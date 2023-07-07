package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"

	"github.com/automatedhome/solar/pkg/evok"
	"github.com/automatedhome/solar/pkg/homeassistant"
	types "github.com/automatedhome/solar/pkg/types"
)

var (
	config             types.Config
	settings           types.Settings
	sensors            types.Sensors
	actuators          types.Actuators
	circuitRunning     bool
	invertFlow         bool
	internalConfigFile string
	lastPass           time.Time
	systemStatus       types.Status
)

var httpClient = &http.Client{}

var (
	heatescapeTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "solar_heat_escape_total",
		Help: "Increase when heat escape system kicked in",
	})
	failsafeTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "solar_failsafe_total",
		Help: "Increase when failsafe system kicked in",
	})
	tankfullTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "solar_tank_full_total",
		Help: "Increase when heating stopped due to tank being full",
	})
	reducedModeMetric = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "solar_reduced_mode",
		Help: "Solar circut is operating in reduced mode",
	})
	flowRate = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "solar_flow_rate_volts",
		Help: "Flow rate in volts",
	})
	circuitRunningMetric = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "solar_circuit_running_binary",
		Help: "Registers when solar control circuit is running",
	})
	controlDelta = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "solar_temperature_delta_celsius",
		Help: "Temperature delta used for setting flow rate",
	})
)

func getSettings() error {
	var errs []error
	var err error

	settings.SolarCritical.Value, err = homeassistant.GetSingleValue(settings.SolarCritical.EntityID)
	if err != nil {
		log.Printf("Could not get setting for solar critical temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.SolarOn.Value, err = homeassistant.GetSingleValue(settings.SolarOn.EntityID)
	if err != nil {
		log.Printf("Could not get setting for solar on temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.SolarOff.Value, err = homeassistant.GetSingleValue(settings.SolarOff.EntityID)
	if err != nil {
		log.Printf("Could not get setting for solar off temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.TankMax.Value, err = homeassistant.GetSingleValue(settings.TankMax.EntityID)
	if err != nil {
		log.Printf("Could not get setting for tank max temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}

	settings.Flow.DutyMin.Value, err = homeassistant.GetSingleValue(settings.Flow.DutyMin.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow duty min from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.Flow.DutyMax.Value, err = homeassistant.GetSingleValue(settings.Flow.DutyMax.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow duty max from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.Flow.TempMin.Value, err = homeassistant.GetSingleValue(settings.Flow.TempMin.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow temp min from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.Flow.TempMax.Value, err = homeassistant.GetSingleValue(settings.Flow.TempMax.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow temp max from Home Assistant: %#v", err)
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d error(s) while fetching settings", len(errs))
	}

	return nil
}

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)

		if err := evok.SetSingleValue(actuators.Pump.Dev, actuators.Pump.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := evok.SetSingleValue(actuators.Switch.Dev, actuators.Switch.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := setFlow(settings.Flow.DutyMin.Value); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		circuitRunning = false
		circuitRunningMetric.Set(0)
	}
}

func start() {
	if !circuitRunning {
		log.Println("Detected optimal conditions. Harvesting.")

		if err := evok.SetSingleValue(actuators.Pump.Dev, actuators.Pump.Circuit, 1); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := evok.SetSingleValue(actuators.Switch.Dev, actuators.Switch.Circuit, 1); err != nil {
			log.Println(err)
			return
		}

		circuitRunning = true
		circuitRunningMetric.Set(1)
		time.Sleep(1 * time.Second)
	}
}

// flow can range from 0 to 10.
func calculateFlow(delta float64) float64 {
	// Flow function:
	// ^ [Flow]                        | s_min, ΔT <= T_min
	// |                    Flow(ΔT) = | A * ΔT + B, A = (s_max - s_min) / (T_max - T_min), B = s_min - T_min * A
	// |       -----------             | s_max, ΔT >= T_max
	// |      /
	// |     /
	// |____/
	// |                  [ΔT]
	// +------------------->
	if delta <= settings.Flow.TempMin.Value {
		return settings.Flow.DutyMin.Value
	}
	if delta >= settings.Flow.TempMax.Value {
		return settings.Flow.DutyMax.Value
	}
	// Flow(ΔT) = a * ΔT + b
	a := (settings.Flow.DutyMax.Value - settings.Flow.DutyMin.Value) / (settings.Flow.TempMax.Value - settings.Flow.TempMin.Value)
	b := settings.Flow.DutyMin.Value - settings.Flow.TempMin.Value*a
	flow := a*delta + b

	if flow > settings.Flow.DutyMax.Value {
		flow = settings.Flow.DutyMax.Value
	}
	if flow < settings.Flow.DutyMin.Value {
		flow = settings.Flow.DutyMin.Value
	}
	return flow
}

func setFlow(value float64) error {
	// TODO: fix this lower in the chain as an actuator is an "inverted" type.
	// Best fix would be to apply this transformation on actuator level. Sadly currently this is not possible without complicating setup.
	if invertFlow {
		value = 10.0 - value
	}

	if err := evok.SetSingleValue(actuators.Flow.Dev, actuators.Flow.Circuit, value); err != nil {
		log.Println(err)
		return err
	}

	flowRate.Set(value)

	return nil
}

func setStatus(s string) {
	systemStatus.Mode = s
	systemStatus.Since = time.Now().Unix()
}

func httpStatus(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(systemStatus)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(js)
	if err != nil {
		log.Println(err)
	}
}

func httpSensors(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(sensors)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(js)
	if err != nil {
		log.Println(err)
	}
}

func httpConfig(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(js)
	if err != nil {
		log.Println(err)
	}
}

func httpHealthCheck(w http.ResponseWriter, r *http.Request) {
	timeout := time.Duration(1 * time.Minute)
	if lastPass.Add(timeout).After(time.Now()) {
		w.WriteHeader(200)
	} else {
		w.WriteHeader(500)
	}
}

func init() {
	circuitRunning = false
	internalConfigFile = "/tmp/config.yaml"

	configFile := flag.String("config", "/config.yaml", "Provide configuration file with MQTT topic mappings")
	invert := flag.Bool("invert", false, "Set this if flow regulator needs to work in 'inverted' mode (when 0V actuator is fully opened)")
	eaddr := flag.String("evok-address", "localhost:8080", "EVOK API address (default: localhost:8080)")
	haddr := flag.String("homeassistant-address", "localhost:8123", "HomeAssistant API address (default: localhost:8123)")
	htoken := flag.String("homeassistant-token", "", "HomeAssistant API token")
	flag.Parse()

	// Set EVOK address
	evok.SetAddress(*eaddr)

	// Set Home Assistant address and token
	homeassistant.SetAddress(*haddr)
	homeassistant.SetToken(*htoken)

	invertFlow = *invert
	if invertFlow {
		log.Println("Setting inverted mode for actuator - higher voltage causes less flow")
	}

	var cfg string
	if _, err := os.Stat(internalConfigFile); err == nil {
		cfg = internalConfigFile
	} else {
		cfg = *configFile
	}

	log.Printf("Reading configuration from %s", cfg)
	data, err := ioutil.ReadFile(cfg)
	if err != nil {
		log.Fatalf("File reading error: %v", err)
		return
	}

	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		log.Fatalf("error: %v", err)
	}
	log.Printf("Reading following config from config file: %#v", config)

	settings = config.Settings
	actuators = config.Actuators
	sensors = config.Sensors
	evok.SetSensors(&sensors)
	evok.InitializeSensorsValues()

	// get configuration values
	err = getSettings()
	if err != nil {
		log.Fatalf("Error getting settings: %v", err)
	}

	setStatus("startup")

	stop("SYSTEM RESET")

}

func main() {
	go func() {
		// Expose metrics
		http.Handle("/metrics", promhttp.Handler())
		// Expose config
		http.HandleFunc("/config", httpConfig)
		// Report current status
		http.HandleFunc("/status", httpStatus)
		// Expose current sensors data
		http.HandleFunc("/sensors", httpSensors)
		// Expose healthcheck
		http.HandleFunc("/health", httpHealthCheck)
		err := http.ListenAndServe(":7001", nil)
		if err != nil {
			panic("HTTP Server for metrics exposition failed: " + err.Error())
		}
	}()

	// periodically refresh settings
	go func() {
		for {
			time.Sleep(5 * time.Minute)
			getSettings()
		}
	}()

	go evok.HandleWebsocketConnection()

	// reductionDuration := time.Duration(config.ReducedTime) * time.Minute
	reductionDuration := 30 * time.Minute
	reducedTill := time.Now()
	reducedMode := false
	delta := 0.0
	for {
		time.Sleep(5 * time.Second)
		lastPass = time.Now()

		delta = (sensors.SolarUp.Value+sensors.SolarOut.Value)/2 - sensors.SolarIn.Value
		controlDelta.Set(delta)

		if sensors.SolarUp.Value >= settings.SolarCritical.Value {
			setStatus("failsafe shutdown")
			stop(fmt.Sprintf("Critical Solar Temperature reached: %f degrees", sensors.SolarUp.Value))
			failsafeTotal.Inc()
			continue
		}

		if sensors.TankUp.Value > settings.TankMax.Value {
			setStatus("tank filled")
			stop(fmt.Sprintf("Tank filled with hot water: %f degrees", sensors.TankUp.Value))
			tankfullTotal.Inc()
			continue
		}

		// heat escape prevention. If delta is less than 0, then system is heating up solar panel
		// calculation need to be based on formula: (solar+out)/2 - in
		if delta < 0 {
			setStatus("heat escape prevention mode")
			stop(fmt.Sprintf("Heat escape prevention, delta: %f < 0", delta))
			heatescapeTotal.Inc()
			continue
		}

		if delta > settings.SolarOff.Value {
			// if sensors.SolarUp.Value-sensors.SolarOut.Value > settings.SolarOn.Value {
			if delta >= settings.SolarOn.Value && sensors.SolarUp.Value > sensors.SolarOut.Value {
				setStatus("working")
				start()
			}
			flow := calculateFlow(delta)
			if err := setFlow(flow); err != nil {
				log.Println(err)
			}
			reducedTill = time.Now().Add(reductionDuration)
		} else if time.Now().Before(reducedTill) {
			// Reduced heat exchange. Set Flow to minimal value.
			if !reducedMode {
				log.Println("Entering reduced heat exchange mode")
				setStatus("reduced mode")
				if err := setFlow(settings.Flow.DutyMin.Value); err != nil {
					log.Println(err)
				} else {
					reducedMode = true
					reducedModeMetric.Set(1)
				}
			}
		} else {
			// Delta SolarIn - SolarOut is too low.
			reducedMode = false
			reducedModeMetric.Set(0)
			setStatus("stopped")
			stop(fmt.Sprintf("Temperature delta too low: %f", delta))
		}
	}
}

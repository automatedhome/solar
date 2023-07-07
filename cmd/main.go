package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/automatedhome/solar/pkg/config"
	"github.com/automatedhome/solar/pkg/evok"
	"github.com/automatedhome/solar/pkg/homeassistant"
)

type Status struct {
	Mode  string `json:"mode"`
	Since int64  `json:"since"`
}

var (
	configs        *config.Config
	circuitRunning bool
	invertFlow     bool
	lastPass       time.Time
	systemStatus   Status

	hassClient *homeassistant.Client
)

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

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)

		if err := evok.SetSingleValue(configs.GetActuators().Pump.Dev, configs.GetActuators().Pump.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := evok.SetSingleValue(configs.GetActuators().Switch.Dev, configs.GetActuators().Switch.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := setFlow(configs.GetSettings().Flow.DutyMin.Value); err != nil {
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

		if err := evok.SetSingleValue(configs.GetActuators().Pump.Dev, configs.GetActuators().Pump.Circuit, 1); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := evok.SetSingleValue(configs.GetActuators().Switch.Dev, configs.GetActuators().Switch.Circuit, 1); err != nil {
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
	flowCfg := configs.GetSettings().Flow

	if delta <= flowCfg.TempMin.Value {
		return flowCfg.DutyMin.Value
	}
	if delta >= flowCfg.TempMax.Value {
		return flowCfg.DutyMax.Value
	}
	// Flow(ΔT) = a * ΔT + b
	a := (flowCfg.DutyMax.Value - flowCfg.DutyMin.Value) / (flowCfg.TempMax.Value - flowCfg.TempMin.Value)
	b := flowCfg.DutyMin.Value - flowCfg.TempMin.Value*a
	flow := a*delta + b

	if flow > flowCfg.DutyMax.Value {
		flow = flowCfg.DutyMax.Value
	}
	if flow < flowCfg.DutyMin.Value {
		flow = flowCfg.DutyMin.Value
	}
	return flow
}

func setFlow(value float64) error {
	// TODO: fix this lower in the chain as an actuator is an "inverted" type.
	// Best fix would be to apply this transformation on actuator level. Sadly currently this is not possible without complicating setup.
	if invertFlow {
		value = 10.0 - value
	}

	if err := evok.SetSingleValue(configs.GetActuators().Flow.Dev, configs.GetActuators().Flow.Circuit, value); err != nil {
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
	internalConfigFile := "/tmp/config.yaml"

	configFile := flag.String("config", "/config.yaml", "Provide configuration file with MQTT topic mappings")
	invert := flag.Bool("invert", false, "Set this if flow regulator needs to work in 'inverted' mode (when 0V actuator is fully opened)")
	eaddr := flag.String("evok-address", "localhost:8080", "EVOK API address (default: localhost:8080)")
	haddr := flag.String("homeassistant-address", "localhost:8123", "HomeAssistant API address (default: localhost:8123)")
	htoken := flag.String("homeassistant-token", "", "HomeAssistant API token")
	flag.Parse()

	// Set EVOK address
	evok.SetAddress(*eaddr)

	// Set Home Assistant address and token
	hassClient = homeassistant.NewClient(*haddr, *htoken)

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

	config.NewConfig(cfg)

	// Initialize sensors addresses. No data is passed at this stage, only configuration.
	sensorsData := *configs.GetSensors()
	// Pass sensors configuration to evok
	evok.SetSensors(&sensorsData)
	// Initialize sensors values
	evok.InitializeSensorsValues()

	// get configuration values
	err := configs.ReadValuesFromHomeAssistant(hassClient)
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
		http.HandleFunc("/config", configs.ExposeSettingsOnHTTP)
		// Report current status
		http.HandleFunc("/status", httpStatus)
		// Expose current sensors data
		http.HandleFunc("/sensors", evok.ExposeSensorsOnHTTP)
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
			configs.ReadValuesFromHomeAssistant(hassClient)
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

		s := evok.GetSensors()
		cfg := configs.GetSettings()

		delta = (s.SolarUp.Value+s.SolarOut.Value)/2 - s.SolarIn.Value
		controlDelta.Set(delta)

		if s.SolarUp.Value >= cfg.SolarCritical.Value {
			setStatus("failsafe shutdown")
			stop(fmt.Sprintf("Critical Solar Temperature reached: %f degrees", s.SolarUp.Value))
			failsafeTotal.Inc()
			continue
		}

		if s.TankUp.Value > cfg.TankMax.Value {
			setStatus("tank filled")
			stop(fmt.Sprintf("Tank filled with hot water: %f degrees", s.TankUp.Value))
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

		if delta > cfg.SolarOff.Value {
			// if sensors.SolarUp.Value-sensors.SolarOut.Value > settings.SolarOn.Value {
			if delta >= cfg.SolarOn.Value && s.SolarUp.Value > s.SolarOut.Value {
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
				if err := setFlow(cfg.Flow.DutyMin.Value); err != nil {
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

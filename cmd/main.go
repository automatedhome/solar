package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/automatedhome/solar/pkg/config"
	"github.com/automatedhome/solar/pkg/evok"
	"github.com/automatedhome/solar/pkg/homeassistant"
)

type Status struct {
	Mode  string  `json:"mode"`
	Since int64   `json:"since"`
	Delta float64 `json:"delta"`
	Flow  float64 `json:"flow"`
}

var (
	promMetrics    *metrics
	circuitRunning bool
	invertFlow     bool
	lastPass       time.Time
	systemStatus   Status

	hass       *homeassistant.Client
	evokClient *evok.Client
)

type metrics struct {
	heatEscapeTotal prometheus.Counter
	failsafeTotal   prometheus.Counter
	tankfullTotal   prometheus.Counter
	reducedMode     prometheus.Gauge
	flowRate        prometheus.Gauge
	circuitRunning  prometheus.Gauge
	controlDelta    prometheus.Gauge
	emergencyTotal  prometheus.Counter
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		heatEscapeTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "solar",
			Name:      "heat_escape_total",
			Help:      "Increase when heat escape system kicked in",
		}),
		failsafeTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "solar",
			Name:      "failsafe_total",
			Help:      "Increase when failsafe system kicked in",
		}),
		tankfullTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "solar",
			Name:      "tank_full_total",
			Help:      "Increase when heating stopped due to tank being full",
		}),
		reducedMode: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "solar",
			Name:      "reduced_mode",
			Help:      "Solar circut is operating in reduced mode",
		}),
		flowRate: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "solar",
			Name:      "flow_rate_volts",
			Help:      "Flow rate in volts",
		}),
		circuitRunning: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "solar",
			Name:      "circuit_running_binary",
			Help:      "Registers when solar control circuit is running",
		}),
		controlDelta: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: "solar",
			Name:      "temperature_delta_celsius",
			Help:      "Temperature delta used for setting flow rate",
		}),
		emergencyTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: "solar",
			Name:      "emergency_total",
			Help:      "Increase when emergency shutoff is triggered",
		}),
	}

	return m
}

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)

		act := evokClient.GetActuators()

		if err := evokClient.SetValue(act.Pump.Dev, act.Pump.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := evokClient.SetValue(act.Switch.Dev, act.Switch.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		minFlow := hass.GetSettings().Flow.DutyMin.Value
		if err := setFlow(minFlow); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		circuitRunning = false
		promMetrics.circuitRunning.Set(0)
	}
}

func start() {
	if !circuitRunning {
		log.Println("Detected optimal conditions. Harvesting.")

		act := evokClient.GetActuators()

		if err := evokClient.SetValue(act.Pump.Dev, act.Pump.Circuit, 1); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := evokClient.SetValue(act.Switch.Dev, act.Switch.Circuit, 1); err != nil {
			log.Println(err)
			return
		}

		circuitRunning = true
		promMetrics.circuitRunning.Set(1)
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
	flowConfig := hass.GetSettings().Flow

	if delta <= flowConfig.TempMin.Value {
		return flowConfig.DutyMin.Value
	}
	if delta >= flowConfig.TempMax.Value {
		return flowConfig.DutyMax.Value
	}
	// Flow(ΔT) = a * ΔT + b
	a := (flowConfig.DutyMax.Value - flowConfig.DutyMin.Value) / (flowConfig.TempMax.Value - flowConfig.TempMin.Value)
	b := flowConfig.DutyMin.Value - flowConfig.TempMin.Value*a
	flow := a*delta + b

	if flow > flowConfig.DutyMax.Value {
		flow = flowConfig.DutyMax.Value
	}
	if flow < flowConfig.DutyMin.Value {
		flow = flowConfig.DutyMin.Value
	}
	return flow
}

func setFlow(value float64) error {
	// FIXME: this is a workaround to scale down the flow to 0 - 10 range. Workaround is necessary as EVOK accepts only
	// values from this range. Addtionally the flow value is rounded.
	value = math.Round(value*10) / 100

	// TODO: fix this lower in the chain as an actuator is an "inverted" type.
	// Best fix would be to apply this transformation on actuator level. Sadly currently this is not possible without complicating setup.
	if invertFlow {
		value = 10.0 - value
	}

	flowConfig := evokClient.GetActuators().Flow
	if err := evokClient.SetValue(flowConfig.Dev, flowConfig.Circuit, value); err != nil {
		log.Println(err)
		return err
	}

	systemStatus.Flow = value
	promMetrics.flowRate.Set(value)

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

	configFile := flag.String("config", "", "Provide configuration file with MQTT topic mappings")
	invert := flag.Bool("invert", false, "Set this if flow regulator needs to work in 'inverted' mode (when 0V actuator is fully opened)")
	eaddr := flag.String("evok-address", "localhost:8080", "EVOK API address (default: localhost:8080)")
	haddr := flag.String("homeassistant-address", "localhost:8123", "HomeAssistant API address (default: localhost:8123)")
	htoken := flag.String("homeassistant-token", "", "HomeAssistant API token")
	flag.Parse()

	invertFlow = *invert
	if invertFlow {
		log.Println("Setting inverted mode for actuator - higher voltage causes less flow")
	}

	// Load configuration
	configClient, err := config.NewConfig(configFile)
	if err != nil {
		log.Fatalf("Error synthesizing configuration: %v", err)
	}

	// Set Home Assistant address, token, and entities configuration
	hass = homeassistant.NewClient(*haddr, *htoken, *configClient.GetSettingsConfig())

	// Initialize configuration values
	err = hass.UpdateAll()
	if err != nil {
		log.Fatalf("Error getting settings from HomeAssistant: %v", err)
	}

	// Set EVOK address and entities configuration
	evokClient = evok.NewClient(*eaddr, *configClient.GetSensorsConfig(), *configClient.GetActuatorsConfig())

	// Initialize sensors values
	err = evokClient.InitializeSensorsValues()
	if err != nil {
		log.Fatalf("Error initializing sensors: %v", err)
	}

	setStatus("startup")

	//circuitRunning = true
	//stop("SYSTEM RESET")
}

func main() {
	reg := prometheus.NewRegistry()
	promMetrics = newMetrics(reg)

	promHandler := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	go func() {
		// Expose metrics
		http.Handle("/metrics", promHandler)
		// Expose config
		http.HandleFunc("/config", hass.ExposeSettingsOnHTTP)
		// Report current status
		http.HandleFunc("/status", httpStatus)
		// Expose current sensors data
		http.HandleFunc("/sensors", evokClient.ExposeSensorsOnHTTP)
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
			time.Sleep(2 * time.Minute)
			err := hass.UpdateAll()
			if err != nil {
				log.Printf("Error getting settings from HomeAssistant: %v", err)
			}
		}
	}()

	go evokClient.HandleWebsocketConnection()

	// reductionDuration := time.Duration(config.ReducedTime) * time.Minute
	reductionDuration := 30 * time.Minute
	reducedTill := time.Now()
	reducedMode := false
	delta := 0.0
	for {
		time.Sleep(5 * time.Second)
		lastPass = time.Now()

		s := evokClient.GetSensors()

		cfg := hass.GetSettings()

		if cfg.SolarEmergency.Value != 0 {
			setStatus("emergency shutoff")
			stop("Emergency shutoff")
			promMetrics.emergencyTotal.Inc()
			continue
		}

		delta = (s.SolarUp.Value+s.SolarOut.Value)/2 - s.SolarIn.Value
		systemStatus.Delta = delta
		promMetrics.controlDelta.Set(delta)

		if s.SolarUp.Value >= cfg.SolarCritical.Value {
			setStatus("failsafe shutdown")
			stop(fmt.Sprintf("Critical Solar Temperature reached: %f degrees", s.SolarUp.Value))
			promMetrics.failsafeTotal.Inc()
			continue
		}

		if s.TankUp.Value > cfg.TankMax.Value {
			setStatus("tank filled")
			stop(fmt.Sprintf("Tank filled with hot water: %f degrees", s.TankUp.Value))
			promMetrics.tankfullTotal.Inc()
			continue
		}

		// heat escape prevention. If delta is less than 0, then system is heating up solar panel
		// calculation need to be based on formula: (solar+out)/2 - in
		if delta < 0 {
			setStatus("heat escape prevention mode")
			stop(fmt.Sprintf("Heat escape prevention, delta: %f < 0", delta))
			promMetrics.heatEscapeTotal.Inc()
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
					promMetrics.reducedMode.Set(1)
				}
			}
		} else {
			// Delta SolarIn - SolarOut is too low.
			reducedMode = false
			promMetrics.reducedMode.Set(0)
			setStatus("stopped")
			stop(fmt.Sprintf("Temperature delta too low: %f", delta))
		}
	}
}

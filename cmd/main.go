package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"

	mqttclient "github.com/automatedhome/common/pkg/mqttclient"
	types "github.com/automatedhome/solar/pkg/types"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var (
	config             types.Config
	settings           types.Settings
	sensors            types.Sensors
	actuators          types.Actuators
	client             mqtt.Client
	circuitRunning     bool
	invertFlow         bool
	internalConfigFile string
	lastPass           time.Time
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

func onMessage(client mqtt.Client, message mqtt.Message) {
	value, err := strconv.ParseFloat(string(message.Payload()), 64)
	if err != nil {
		log.Printf("Received incorrect message payload: '%v'\n", message.Payload())
		return
	}
	switch message.Topic() {
	case sensors.SolarUp.Address:
		sensors.SolarUp.Value = value
	case sensors.SolarIn.Address:
		sensors.SolarIn.Value = value
	case sensors.SolarOut.Address:
		sensors.SolarOut.Value = value
	case sensors.TankUp.Address:
		sensors.TankUp.Value = value
	case settings.SolarCritical.Address:
		settings.SolarCritical.Value = value
		dumpConfig()
	case settings.SolarOn.Address:
		settings.SolarOn.Value = value
		dumpConfig()
	case settings.SolarOff.Address:
		settings.SolarOff.Value = value
		dumpConfig()
	case settings.TankMax.Address:
		settings.TankMax.Value = value
		dumpConfig()
	case settings.Flow.DutyMin.Address:
		settings.Flow.DutyMin.Value = value
		dumpConfig()
	case settings.Flow.DutyMax.Address:
		settings.Flow.DutyMax.Value = value
		dumpConfig()
	case settings.Flow.TempMin.Address:
		settings.Flow.TempMin.Value = value
		dumpConfig()
	case settings.Flow.TempMax.Address:
		settings.Flow.TempMax.Value = value
		dumpConfig()
	}
}

func waitForData(lockValue float64) {
	// Wait for sensors data
	for {
		if sensors.SolarIn.Value != lockValue && sensors.SolarOut.Value != lockValue && sensors.SolarUp.Value != lockValue && sensors.TankUp.Value != lockValue {
			break
		}
		msg := []string{"Waiting 30s for sensors data. Currently lacking:"}
		if sensors.SolarIn.Value == 300 {
			msg = append(msg, "solarIn")
		}
		if sensors.SolarOut.Value == 300 {
			msg = append(msg, "solarOut")
		}
		if sensors.SolarUp.Value == 300 {
			msg = append(msg, "solarUp")
		}
		if sensors.TankUp.Value == 300 {
			msg = append(msg, "tankUp")
		}
		log.Println(strings.Join(msg, " "))
		time.Sleep(30 * time.Second)
	}
	log.Printf("Starting with sensors data received: %+v\n", sensors)
}

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)

		if err := mqttclient.Publish(client, actuators.Pump, 0, false, "0"); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := mqttclient.Publish(client, actuators.Sw, 0, false, "0"); err != nil {
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

		if err := mqttclient.Publish(client, actuators.Pump, 0, false, "1"); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := mqttclient.Publish(client, actuators.Sw, 0, false, "1"); err != nil {
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
	err := mqttclient.Publish(client, actuators.Flow, 0, false, fmt.Sprintf("%.1f", value))
	flowRate.Set(value)
	if err != nil {
		return err
	}

	return nil
}

func httpConfig(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(settings)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write(js)
}

func httpHealthCheck(w http.ResponseWriter, r *http.Request) {
	timeout := time.Duration(1 * time.Minute)
	if lastPass.Add(timeout).After(time.Now()) {
		w.WriteHeader(200)
	} else {
		w.WriteHeader(500)
	}
}

func dumpConfig() {
	var cfg types.Config
	cfg.Sensors = sensors
	cfg.Actuators = actuators
	cfg.Settings = settings

	d, err := yaml.Marshal(&cfg)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	err = ioutil.WriteFile(internalConfigFile, d, 0644)
	if err != nil {
		log.Fatal(err)
	}
}

func init() {
	circuitRunning = false
	internalConfigFile = "/tmp/config.yaml"

	broker := flag.String("broker", "tcp://127.0.0.1:1883", "The full url of the MQTT server to connect to ex: tcp://127.0.0.1:1883")
	clientID := flag.String("clientid", "solar", "A clientid for the connection")
	configFile := flag.String("config", "/config.yaml", "Provide configuration file with MQTT topic mappings")
	invert := flag.Bool("invert", false, "Set this if flow regulator needs to work in 'inverted' mode (when 0V actuator is fully opened)")
	flag.Parse()

	invertFlow = *invert
	if invertFlow {
		log.Println("Setting inverted mode for actuator - higher voltage causes less flow")
	}

	brokerURL, err := url.Parse(*broker)
	if err != nil {
		log.Fatal(err)
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

	// set initial sensors values and ignore ones provided by config file
	// this is used as a locking mechanism to prevent starting control loop without current sensors data
	lockTemp := 300.0
	sensors.SolarUp.Value = lockTemp
	sensors.SolarIn.Value = lockTemp
	sensors.SolarOut.Value = lockTemp
	sensors.TankUp.Value = lockTemp

	// subscribe to topics
	var topics []string
	topics = append(topics, sensors.SolarIn.Address, sensors.SolarOut.Address, sensors.SolarUp.Address, sensors.TankUp.Address)
	topics = append(topics, settings.SolarCritical.Address, settings.SolarOn.Address, settings.SolarOff.Address, settings.TankMax.Address)
	topics = append(topics, settings.Flow.TempMin.Address, settings.Flow.TempMax.Address, settings.Flow.DutyMin.Address, settings.Flow.DutyMax.Address)
	client = mqttclient.New(*clientID, brokerURL, topics, onMessage)
	log.Printf("Connected to %s as %s and waiting for messages\n", *broker, *clientID)

	stop("SYSTEM RESET")

	// Wait for sensors data
	waitForData(lockTemp)

	// Write config file
	dumpConfig()
}

func main() {
	go func() {
		// Expose metrics
		http.Handle("/metrics", promhttp.Handler())
		// Expose config
		http.HandleFunc("/config", httpConfig)
		// Expose healthcheck
		http.HandleFunc("/health", httpHealthCheck)
		err := http.ListenAndServe(":7001", nil)
		if err != nil {
			panic("HTTP Server for metrics exposition failed: " + err.Error())
		}
	}()

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
			stop(fmt.Sprintf("Critical Solar Temperature reached: %f degrees", sensors.SolarUp.Value))
			failsafeTotal.Inc()
			continue
		}

		if sensors.TankUp.Value > settings.TankMax.Value {
			stop(fmt.Sprintf("Tank filled with hot water: %f degrees", sensors.TankUp.Value))
			tankfullTotal.Inc()
			continue
		}

		// heat escape prevention. If delta is less than 0, then system is heating up solar panel
		// calculation need to be based on formula: (solar+out)/2 - in
		if delta < 0 {
			stop(fmt.Sprintf("Heat escape prevention, delta: %f < 0", delta))
			heatescapeTotal.Inc()
			continue
		}

		if delta > settings.SolarOff.Value {
			// if sensors.SolarUp.Value-sensors.SolarOut.Value > settings.SolarOn.Value {
			if delta >= settings.SolarOn.Value && sensors.SolarUp.Value > sensors.SolarOut.Value {
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
			stop(fmt.Sprintf("Temperature delta too low: %f", delta))
		}
	}
}

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v2"

	mqttclient "github.com/automatedhome/common/pkg/mqttclient"
	types "github.com/automatedhome/solar/pkg/types"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

var (
	config         types.Config
	settings       types.Settings
	sensors        types.Sensors
	actuators      types.Actuators
	client         mqtt.Client
	circuitRunning bool
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
	case settings.SolarOn.Address:
		settings.SolarOn.Value = value
	case settings.SolarOff.Address:
		settings.SolarOff.Value = value
	case settings.TankMax.Address:
		settings.TankMax.Value = value
	case settings.Flow.DutyMin.Address:
		settings.Flow.DutyMin.Value = value
	case settings.Flow.DutyMax.Address:
		settings.Flow.DutyMax.Value = value
	case settings.Flow.TempMin.Address:
		settings.Flow.TempMin.Value = value
	case settings.Flow.TempMax.Address:
		settings.Flow.TempMax.Value = value
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

		if err := mqttclient.Publish(client, actuators.Flow, 0, false, fmt.Sprintf("%.2f", settings.Flow.DutyMin.Value)); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		circuitRunning = false
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
		time.Sleep(1 * time.Second)

		circuitRunning = true
	}
}

// flow can range from 0 to 10.
func calculateFlow() float64 {
	// Flow function:
	// ^ [Flow]                        | s_min, ΔT <= T_min
	// |                    Flow(ΔT) = | A * ΔT + B, A = (s_max - s_min) / (T_max - T_min), B = s_min - T_min * A
	// |       -----------             | s_max, ΔT >= T_max
	// |      /
	// |     /
	// |____/
	// |                  [ΔT]
	// +------------------->
	delta := sensors.SolarIn.Value - sensors.SolarOut.Value
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

	log.Printf("Setting flow to %.2f", flow)
	return flow
}

func failsafe(solar float64, solarCritical float64) bool {
	if solar >= solarCritical {
		stop("Critical Solar Temperature reached")
		return true
	}
	return false
}

func heatescape(solar float64, in float64) bool {
	if solar < in {
		stop("Heat escape prevention (Tin >= TSolar)")
		return true
	}
	return false
}

func tankfull(tank float64, max float64) bool {
	if tank > max {
		stop("Tank filled with hot water")
		return true
	}
	return false
}

func getDelta(solar float64, in float64, out float64) float64 {
	if solar >= out {
		return (solar+out)/2 - in
	}
	return solar - in
}

func init() {
	circuitRunning = false

	broker := flag.String("broker", "tcp://127.0.0.1:1883", "The full url of the MQTT server to connect to ex: tcp://127.0.0.1:1883")
	clientID := flag.String("clientid", "Solar", "A clientid for the connection")
	configFile := flag.String("config", "/config.yaml", "Provide configuration file with MQTT topic mappings")
	flag.Parse()

	brokerURL, err := url.Parse(*broker)
	if err != nil {
		log.Fatal(err)
	}

	log.Printf("Reading configuration from %s", *configFile)
	data, err := ioutil.ReadFile(*configFile)
	if err != nil {
		log.Fatalf("File reading error: %v", err)
		return
	}
	log.Printf("Reading following config from config file: %+v", data)

	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		log.Fatalf("error: %v", err)
	}

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

	stop("reset system")

	// Wait for sensors data
	waitForData(lockTemp)
}

func main() {
	// reductionDuration := time.Duration(config.ReducedTime) * time.Minute
	reductionDuration := 30 * time.Minute
	reducedTill := time.Now()
	reducedMode := false
	delta := 0.0
	lastFlow := 0.0
	for {
		time.Sleep(1 * time.Second)

		if failsafe(sensors.SolarUp.Value, settings.SolarCritical.Value) {
			continue
		}

		if heatescape(sensors.SolarUp.Value, sensors.SolarIn.Value) {
			continue
		}

		if tankfull(sensors.TankUp.Value, settings.TankMax.Value) {
			continue
		}

		delta = getDelta(sensors.SolarUp.Value, sensors.SolarIn.Value, sensors.SolarOut.Value)

		if delta >= settings.SolarOff.Value {
			if sensors.SolarUp.Value-sensors.SolarOut.Value > settings.SolarOn.Value {
				start()
			}
			flow := calculateFlow()
			if flow != lastFlow {
				if err := mqttclient.Publish(client, actuators.Flow, 0, false, fmt.Sprintf("%.2f", flow)); err == nil {
					lastFlow = flow
				}
			}
			reducedTill = time.Now().Add(reductionDuration)
		} else if time.Now().Before(reducedTill) {
			// Reduced heat exchange. Set Flow to minimal value.
			if !reducedMode {
				log.Println("Entering reduced heat exchange mode.")
				if err := mqttclient.Publish(client, actuators.Flow, 0, false, fmt.Sprintf("%.2f", settings.Flow.DutyMin.Value)); err == nil {
					reducedMode = true
				}
			}
		} else {
			// Delta SolarIn - SolarOut is too low.
			reducedMode = false
			stop("In-Out delta too low.")
		}
	}
}

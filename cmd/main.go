package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"strconv"
	"time"

	mqttclient "github.com/automatedhome/flow-meter/pkg/mqttclient"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type DataPoint struct {
	v    float64
	addr string
}

type FlowRegulator struct {
	dutyMin DataPoint
	tempMin DataPoint
	dutyMax DataPoint
	tempMax DataPoint
}

type Settings struct {
	solarCritical DataPoint
	solarOn       DataPoint
	solarOff      DataPoint
	tankMax       DataPoint
	flow          FlowRegulator
}

type Sensors struct {
	solarUp  DataPoint
	solarIn  DataPoint
	solarOut DataPoint
	tankUp   DataPoint
}

type Actuators struct {
	pump string
	sw   string
	flow string
}

var settings Settings
var sensors Sensors
var actuators Actuators
var client mqtt.Client
var circuitRunning bool

func onMessage(client mqtt.Client, message mqtt.Message) {
	value, err := strconv.ParseFloat(string(message.Payload()), 64)
	if err != nil {
		log.Printf("Received incorrect message payload: '%v'\n", message.Payload())
		return
	}
	switch message.Topic() {
	case sensors.solarUp.addr:
		sensors.solarUp.v = value
	case sensors.solarIn.addr:
		sensors.solarIn.v = value
	case sensors.solarOut.addr:
		sensors.solarOut.v = value
	case sensors.tankUp.addr:
		sensors.tankUp.v = value
	case settings.solarCritical.addr:
		settings.solarCritical.v = value
	case settings.solarOn.addr:
		settings.solarOn.v = value
	case settings.solarOff.addr:
		settings.solarOff.v = value
	case settings.tankMax.addr:
		settings.tankMax.v = value
	case settings.flow.dutyMin.addr:
		settings.flow.dutyMin.v = value
	case settings.flow.dutyMax.addr:
		settings.flow.dutyMax.v = value
	case settings.flow.tempMin.addr:
		settings.flow.tempMin.v = value
	case settings.flow.tempMax.addr:
		settings.flow.tempMax.v = value
	}
}

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)
		client.Publish(actuators.pump, 0, false, "0")
		client.Publish(actuators.sw, 0, false, "0")
		client.Publish(actuators.flow, 0, false, fmt.Sprintf("%.2f", settings.flow.dutyMin.v))
		circuitRunning = false
	}
}

func start() {
	if !circuitRunning {
		log.Println("Detected optimal conditions. Harvesting.")
		client.Publish(actuators.pump, 0, false, "1")
		client.Publish(actuators.sw, 0, false, "1")
		circuitRunning = true
	}
}

func calculateFlow() float64 {
	// Flow function:
	// ^ [flow]                        | s_min, ΔT <= T_min
	// |                    flow(ΔT) = | A * ΔT + B, A = (s_max - s_min) / (T_max - T_min), B = s_min - T_min * A
	// |       -----------             | s_max, ΔT >= T_max
	// |      /
	// |     /
	// |____/
	// |                  [ΔT]
	// +------------------->
	delta := sensors.solarIn.v - sensors.solarOut.v
	if delta <= settings.flow.tempMin.v {
		return settings.flow.dutyMin.v
	}
	if delta >= settings.flow.tempMax.v {
		return settings.flow.dutyMax.v
	}
	// flow(ΔT) = a * ΔT + b
	a := (settings.flow.dutyMax.v - settings.flow.dutyMin.v) / (settings.flow.tempMax.v - settings.flow.tempMin.v)
	b := settings.flow.dutyMin.v - settings.flow.tempMin.v*a
	return a*delta + b
}

func init() {
	// TODO read it from yaml file
	settings = Settings{}
	sensors = Sensors{}
	actuators = Actuators{}
	actuators.flow = "solar/actuators/flow" // proxy to "evok/ao/1/set"
	actuators.pump = "solar/actuators/pump" // proxy to "evok/relay/3/set"
	actuators.sw = "solar/actuators/switch" // proxy to "evok/relay/2/set"

	sensors.solarIn = DataPoint{0, "solar/temperature/in"}
	sensors.solarOut = DataPoint{0, "solar/temperature/out"}
	sensors.solarUp = DataPoint{0, "solar/temperature/up"}
	sensors.tankUp = DataPoint{0, "tank/temperature/up"}

	settings.solarCritical = DataPoint{90, "solar/settings/critical"}
	settings.solarOn = DataPoint{8, "solar/settings/on"}
	settings.solarOff = DataPoint{5, "solar/settings/off"}
	settings.tankMax = DataPoint{65, "solar/settings/tank"}

	settings.flow.tempMin = DataPoint{5, "solar/settings/flow/t_min"}
	settings.flow.tempMax = DataPoint{9, "solar/settings/flow/t_max"}
	settings.flow.dutyMin = DataPoint{1.8, "solar/settings/flow/d_min"} // min 0
	settings.flow.dutyMax = DataPoint{3, "solar/settings/flow/d_max"}   // max 10

}

func main() {
	broker := flag.String("broker", "tcp://127.0.0.1:1883", "The full url of the MQTT server to connect to ex: tcp://127.0.0.1:1883")
	clientID := flag.String("clientid", "solar", "A clientid for the connection")
	flag.Parse()

	brokerURL, err := url.Parse(*broker)
	if err != nil {
		log.Fatal(err)
	}

	// subscribe to topics
	var topics []string
	topics = append(topics, actuators.flow)
	topics = append(topics, actuators.pump)
	topics = append(topics, actuators.sw)
	topics = append(topics, sensors.solarIn.addr)
	topics = append(topics, sensors.solarOut.addr)
	topics = append(topics, sensors.solarUp.addr)
	topics = append(topics, sensors.tankUp.addr)
	topics = append(topics, settings.solarCritical.addr)
	topics = append(topics, settings.solarOn.addr)
	topics = append(topics, settings.solarOff.addr)
	topics = append(topics, settings.tankMax.addr)
	topics = append(topics, settings.flow.tempMin.addr)
	topics = append(topics, settings.flow.tempMax.addr)
	topics = append(topics, settings.flow.dutyMin.addr)
	topics = append(topics, settings.flow.dutyMax.addr)
	client = mqttclient.New(*clientID, brokerURL, topics, onMessage)
	log.Printf("Connected to %s as %s and waiting for messages\n", *broker, *clientID)

	// Wait for sensors data
	for {
		if sensors.solarIn.v != 0 && sensors.solarOut.v != 0 && sensors.solarUp.v != 0 && sensors.tankUp.v != 0 {
			break
		}
		log.Println("Waiting 15s for sensors data...")
		time.Sleep(15 * time.Second)
	}
	log.Println("Starting with sensors data received: %+v", sensors)

	// Step 2. - RUN forever
	reducedTill := time.Now().Add(30 * time.Minute)
	reducedSent := false
	delta := 0.0
	lastFlow := 0.0
	for {
		time.Sleep(1 * time.Second)

		if sensors.solarUp.v >= settings.solarCritical.v {
			stop("Critical solar temperature reached")
			continue
		}

		if sensors.solarOut.v >= sensors.solarUp.v {
			stop("Heat escape prevention (Tout >= Tsolar)")
			continue
		}
		if sensors.tankUp.v > settings.tankMax.v {
			stop("Tank filled with hot water")
			continue
		}

		if sensors.solarUp.v >= sensors.solarIn.v {
			delta = (sensors.solarUp.v+sensors.solarIn.v)/2 - sensors.solarOut.v
		} else {
			delta = sensors.solarUp.v - sensors.solarOut.v
		}

		if delta >= settings.solarOff.v {
			if sensors.solarUp.v-sensors.solarOut.v > settings.solarOn.v {
				start()
			}
			flow := calculateFlow()
			if flow == lastFlow {
				client.Publish(actuators.sw, 0, false, fmt.Sprintf("%.2f", flow))
				lastFlow = flow
			}
			reducedTill = time.Now().Add(30 * time.Minute)
		} else if time.Now().Before(reducedTill) {
			// Reduced heat exchange. Set flow to minimal value.
			if !reducedSent {
				log.Println("Entering reduced heat exchange mode.")
				client.Publish(actuators.flow, 0, false, fmt.Sprintf("%.2f", settings.flow.dutyMin.v))
				reducedSent = true
			}
		} else {
			// Delta solarIn - solarOut is too low.
			reducedSent = false
			stop("In-Out delta too low.")
		}
	}
}

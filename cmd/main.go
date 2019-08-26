package main

import (
	"flag"
	"fmt"
	"log"
	"net/url"
	"strings"
	"strconv"
	"time"

	mqttclient "github.com/automatedhome/common/pkg/mqttclient"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type DataPoint struct {
	val  float64
	addr string
}

type Settings struct {
	solarCritical DataPoint
	solarOn       DataPoint
	solarOff      DataPoint
	tankMax       DataPoint
	flow          struct {
		dutyMin DataPoint
		tempMin DataPoint
		dutyMax DataPoint
		tempMax DataPoint
	} `yaml:"flow"`
}

type Sensors struct {
	solarUp  DataPoint
	solarIn  DataPoint
	solarOut DataPoint
	tankUp   DataPoint
}

type Actuators struct {
	pump string `yaml:"pump"`
	sw   string `yaml:"switch"`
	flow string `yaml:"flow"`
}

type Config struct {
	actuators Actuators `yaml:"actuators"`
	sensors   Sensors   `yaml:"sensors"`
	settings  Settings  `yaml:"settings"`
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
		sensors.solarUp.val = value
	case sensors.solarIn.addr:
		sensors.solarIn.val = value
	case sensors.solarOut.addr:
		sensors.solarOut.val = value
	case sensors.tankUp.addr:
		sensors.tankUp.val = value
	case settings.solarCritical.addr:
		settings.solarCritical.val = value
	case settings.solarOn.addr:
		settings.solarOn.val = value
	case settings.solarOff.addr:
		settings.solarOff.val = value
	case settings.tankMax.addr:
		settings.tankMax.val = value
	case settings.flow.dutyMin.addr:
		settings.flow.dutyMin.val = value
	case settings.flow.dutyMax.addr:
		settings.flow.dutyMax.val = value
	case settings.flow.tempMin.addr:
		settings.flow.tempMin.val = value
	case settings.flow.tempMax.addr:
		settings.flow.tempMax.val = value
	}
}

func stop(reason string) {
	if circuitRunning {
		// Adding sleep between sending data to prevent race conditions in mqttmapper service
		log.Println("Stopping: " + reason)
		client.Publish(actuators.pump, 0, false, "0")
		time.Sleep(1 * time.Second)
		client.Publish(actuators.sw, 0, false, "0")
		time.Sleep(1 * time.Second)
		client.Publish(actuators.flow, 0, false, fmt.Sprintf("%.2f", settings.flow.dutyMin.val))
		circuitRunning = false
	}
}

func start() {
	if !circuitRunning {
		// Adding sleep between sending data to prevent race conditions in mqttmapper service
		log.Println("Detected optimal conditions. Harvesting.")
		client.Publish(actuators.pump, 0, false, "1")
		time.Sleep(1 * time.Second)
		client.Publish(actuators.sw, 0, false, "1")
		time.Sleep(1 * time.Second)
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
	delta := sensors.solarIn.val - sensors.solarOut.val
	if delta <= settings.flow.tempMin.val {
		return settings.flow.dutyMin.val
	}
	if delta >= settings.flow.tempMax.val {
		return settings.flow.dutyMax.val
	}
	// flow(ΔT) = a * ΔT + b
	a := (settings.flow.dutyMax.val - settings.flow.dutyMin.val) / (settings.flow.tempMax.val - settings.flow.tempMin.val)
	b := settings.flow.dutyMin.val - settings.flow.tempMin.val*a
	return a*delta + b
}

func init() {
	// TODO read it from yaml file
	settings = Settings{}
	sensors = Sensors{}
	actuators = Actuators{}
	// TODO uncomment after acceptance testing
	//actuators.flow = "solar/actuators/flow" // proxy to "evok/ao/1/set" and later to flow-controller
	//actuators.pump = "solar/actuators/pump" // proxy to "evok/relay/3/set"
	//actuators.sw = "solar/actuators/switch" // proxy to "evok/relay/2/set"
	actuators.flow = "evok/ao/1/set"
	actuators.pump = "evok/relay/3/set"
	actuators.sw = "evok/relay/2/set"

	sensors.solarIn = DataPoint{300, "solar/temperature/in"}
	sensors.solarOut = DataPoint{300, "solar/temperature/out"}
	sensors.solarUp = DataPoint{300, "solar/temperature/up"}
	sensors.tankUp = DataPoint{300, "tank/temperature/up"}

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
	topics = append(topics, sensors.solarIn.addr, sensors.solarOut.addr, sensors.solarUp.addr, sensors.tankUp.addr)
	topics = append(topics, settings.solarCritical.addr, settings.solarOn.addr, settings.solarOff.addr, settings.tankMax.addr)
	topics = append(topics, settings.flow.tempMin.addr, settings.flow.tempMax.addr, settings.flow.dutyMin.addr, settings.flow.dutyMax.addr)
	client = mqttclient.New(*clientID, brokerURL, topics, onMessage)
	log.Printf("Connected to %s as %s and waiting for messages\n", *broker, *clientID)

	msg := []string{"Waiting 15s for sensors data. Currently lacking:"}
	// Wait for sensors data
	for {
		if sensors.solarIn.val != 300 && sensors.solarOut.val != 300 && sensors.solarUp.val != 300 && sensors.tankUp.val != 300 {
			break
		}
		if sensors.solarIn.val == 300 { msg = append(msg, "solarIn") }
		if sensors.solarOut.val == 300 { msg = append(msg, "solarOut") }
		if sensors.solarUp.val == 300 { msg = append(msg, "solarUp") }
		if sensors.tankUp.val == 300 { msg = append(msg, "tankUp") }
		log.Println(strings.Join(msg, " "))
		time.Sleep(15 * time.Second)
	}
	log.Printf("Starting with sensors data received: %+v\n", sensors)

	// Step 2. - RUN forever
	reducedTill := time.Now().Add(30 * time.Minute)
	reducedSent := false
	delta := 0.0
	lastFlow := 0.0
	for {
		time.Sleep(1 * time.Second)

		if sensors.solarUp.val >= settings.solarCritical.val {
			stop("Critical solar temperature reached")
			continue
		}

		if sensors.solarOut.val >= sensors.solarUp.val {
			stop("Heat escape prevention (Tout >= Tsolar)")
			continue
		}
		if sensors.tankUp.val > settings.tankMax.val {
			stop("Tank filled with hot water")
			continue
		}

		if sensors.solarUp.val >= sensors.solarIn.val {
			delta = (sensors.solarUp.val+sensors.solarIn.val)/2 - sensors.solarOut.val
		} else {
			delta = sensors.solarUp.val - sensors.solarOut.val
		}

		if delta >= settings.solarOff.val {
			if sensors.solarUp.val-sensors.solarOut.val > settings.solarOn.val {
				start()
			}
			flow := calculateFlow()
			if flow != lastFlow {
				client.Publish(actuators.flow, 0, false, fmt.Sprintf("%.2f", flow))
				lastFlow = flow
			}
			reducedTill = time.Now().Add(30 * time.Minute)
		} else if time.Now().Before(reducedTill) {
			// Reduced heat exchange. Set flow to minimal value.
			if !reducedSent {
				log.Println("Entering reduced heat exchange mode.")
				client.Publish(actuators.flow, 0, false, fmt.Sprintf("%.2f", settings.flow.dutyMin.val))
				reducedSent = true
			}
		} else {
			// Delta solarIn - solarOut is too low.
			reducedSent = false
			stop("In-Out delta too low.")
		}
	}
}

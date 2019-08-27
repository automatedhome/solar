package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"strconv"
	"time"

	"gopkg.in/yaml.v2"

	mqttclient "github.com/automatedhome/common/pkg/mqttclient"
	mqtt "github.com/eclipse/paho.mqtt.golang"
)

type DataPoint struct {
	Val  float64 `yaml:"value,omitempty"`
	Addr string  `yaml:"address"`
}

type Settings struct {
	SolarCritical DataPoint `yaml:"solarCritical"`
	SolarOn       DataPoint `yaml:"solarOn"`
	SolarOff      DataPoint `yaml:"solarOff"`
	TankMax       DataPoint `yaml:"tankMax"`
	Flow          struct {
		DutyMin DataPoint `yaml:"dutyMin"`
		TempMin DataPoint `yaml:"tempMin"`
		DutyMax DataPoint `yaml:"dutyMax"`
		TempMax DataPoint `yaml:"tempMax"`
	} `yaml:"flow"`
}

type Sensors struct {
	SolarUp  DataPoint `yaml:"solarUp"`
	SolarIn  DataPoint `yaml:"solarIn"`
	SolarOut DataPoint `yaml:"solarOut"`
	TankUp   DataPoint `yaml:"tankUp"`
}

type Actuators struct {
	Pump string `yaml:"pump"`
	Sw   string `yaml:"switch"`
	Flow string `yaml:"flow"`
}

type Config struct {
	Actuators Actuators `yaml:"actuators"`
	Sensors   Sensors   `yaml:"sensors"`
	Settings  Settings  `yaml:"settings"`
}

var config Config
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
	case sensors.SolarUp.Addr:
		sensors.SolarUp.Val = value
	case sensors.SolarIn.Addr:
		sensors.SolarIn.Val = value
	case sensors.SolarOut.Addr:
		sensors.SolarOut.Val = value
	case sensors.TankUp.Addr:
		sensors.TankUp.Val = value
	case settings.SolarCritical.Addr:
		settings.SolarCritical.Val = value
	case settings.SolarOn.Addr:
		settings.SolarOn.Val = value
	case settings.SolarOff.Addr:
		settings.SolarOff.Val = value
	case settings.TankMax.Addr:
		settings.TankMax.Val = value
	case settings.Flow.DutyMin.Addr:
		settings.Flow.DutyMin.Val = value
	case settings.Flow.DutyMax.Addr:
		settings.Flow.DutyMax.Val = value
	case settings.Flow.TempMin.Addr:
		settings.Flow.TempMin.Val = value
	case settings.Flow.TempMax.Addr:
		settings.Flow.TempMax.Val = value
	}
}

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)
		client.Publish(actuators.Pump, 0, false, "0")
		client.Publish(actuators.Sw, 0, false, "0")
		client.Publish(actuators.Flow, 0, false, fmt.Sprintf("%.2f", settings.Flow.DutyMin.Val))
		circuitRunning = false
	}
}

func start() {
	if !circuitRunning {
		log.Println("Detected optimal conditions. Harvesting.")
		client.Publish(actuators.Pump, 0, false, "1")
		client.Publish(actuators.Sw, 0, false, "1")
		circuitRunning = true
	}
}

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
	delta := sensors.SolarIn.Val - sensors.SolarOut.Val
	if delta <= settings.Flow.TempMin.Val {
		return settings.Flow.DutyMin.Val
	}
	if delta >= settings.Flow.TempMax.Val {
		return settings.Flow.DutyMax.Val
	}
	// Flow(ΔT) = a * ΔT + b
	a := (settings.Flow.DutyMax.Val - settings.Flow.DutyMin.Val) / (settings.Flow.TempMax.Val - settings.Flow.TempMin.Val)
	b := settings.Flow.DutyMin.Val - settings.Flow.TempMin.Val*a
	return a*delta + b
}

func main() {
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

	err = yaml.UnmarshalStrict(data, &config)
	//err = yaml.Unmarshal(data, &config)
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	settings = config.Settings
	actuators = config.Actuators
	sensors = config.Sensors
	// set initial sensors values and ignore ones provided by config file
	// this is used as a locking mechanism to prevent starting control loop without current sensors data
	lockTemp := 300.0
	sensors.SolarUp.Val = lockTemp
	sensors.SolarIn.Val = lockTemp
	sensors.SolarOut.Val = lockTemp
	sensors.TankUp.Val = lockTemp

	// subscribe to topics
	var topics []string
	topics = append(topics, sensors.SolarIn.Addr, sensors.SolarOut.Addr, sensors.SolarUp.Addr, sensors.TankUp.Addr)
	topics = append(topics, settings.SolarCritical.Addr, settings.SolarOn.Addr, settings.SolarOff.Addr, settings.TankMax.Addr)
	topics = append(topics, settings.Flow.TempMin.Addr, settings.Flow.TempMax.Addr, settings.Flow.DutyMin.Addr, settings.Flow.DutyMax.Addr)
	client = mqttclient.New(*clientID, brokerURL, topics, onMessage)
	log.Printf("Connected to %s as %s and waiting for messages\n", *broker, *clientID)

	// Wait for sensors data
	for {
		if sensors.SolarIn.Val != lockTemp && sensors.SolarOut.Val != lockTemp && sensors.SolarUp.Val != lockTemp && sensors.TankUp.Val != lockTemp {
			break
		}
		log.Println("Waiting 15s for sensors data...")
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

		if sensors.SolarUp.Val >= settings.SolarCritical.Val {
			stop("Critical Solar Temperature reached")
			continue
		}

		if sensors.SolarOut.Val >= sensors.SolarUp.Val {
			stop("Heat escape prevention (Tout >= TSolar)")
			continue
		}
		if sensors.TankUp.Val > settings.TankMax.Val {
			stop("Tank filled with hot water")
			continue
		}

		if sensors.SolarUp.Val >= sensors.SolarIn.Val {
			delta = (sensors.SolarUp.Val+sensors.SolarIn.Val)/2 - sensors.SolarOut.Val
		} else {
			delta = sensors.SolarUp.Val - sensors.SolarOut.Val
		}

		if delta >= settings.SolarOff.Val {
			if sensors.SolarUp.Val-sensors.SolarOut.Val > settings.SolarOn.Val {
				start()
			}
			Flow := calculateFlow()
			if Flow != lastFlow {
				client.Publish(actuators.Flow, 0, false, fmt.Sprintf("%.2f", Flow))
				lastFlow = Flow
			}
			reducedTill = time.Now().Add(30 * time.Minute)
		} else if time.Now().Before(reducedTill) {
			// Reduced heat exchange. Set Flow to minimal value.
			if !reducedSent {
				log.Println("Entering reduced heat exchange mode.")
				client.Publish(actuators.Flow, 0, false, fmt.Sprintf("%.2f", settings.Flow.DutyMin.Val))
				reducedSent = true
			}
		} else {
			// Delta SolarIn - SolarOut is too low.
			reducedSent = false
			stop("In-Out delta too low.")
		}
	}
}

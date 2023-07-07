package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"gopkg.in/yaml.v2"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"

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
	systemStatus       types.Status
	evokAddress        string
	//homeAssistantAddress string
	//homeAssistantToken   string
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
	solarPanelVoltage = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "solar_panel_voltage_volts",
		Help: "Voltage reported by solar panel temperature sensor",
	})
	solarPanelTemperature = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "solar_panel_temperature_celsius",
		Help: "Temperature of solar panel",
	})
)

func handleWebsocketMessage(address string) {
	fmt.Printf("Connecting to EVOK at %s\n", address)

	conn, _, _, err := ws.DefaultDialer.Dial(context.TODO(), "ws://"+evokAddress+"/ws")
	if err != nil {
		panic("Connecting to EVOK failed: " + err.Error())
	}
	defer conn.Close()

	msg := "{\"cmd\":\"filter\", \"devices\":[\"ai\",\"temp\"]}"
	//msg := "{\"cmd\":\"all\"}" // FIXME: This is a temporary hack to get all data from EVOK
	if err = wsutil.WriteClientMessage(conn, ws.OpText, []byte(msg)); err != nil {
		panic("Sending websocket message to EVOK failed: " + err.Error())
	}

	var inputs []types.EvokDevice
	for {
		payload, err := wsutil.ReadServerText(conn)
		if err != nil {
			log.Printf("Received incorrect data: %#v", err)
		}

		if err := json.Unmarshal(payload, &inputs); err != nil {
			log.Printf("Could not parse received data: %#v", err)
		}

		parseEvokData(inputs)
	}
}

func parseEvokData(data []types.EvokDevice) {
	for _, msg := range data {
		if msg.Circuit == sensors.SolarUp.Circuit && msg.Dev == sensors.SolarUp.Dev {
			temp := calculateTemperature(msg.Value)
			sensors.SolarUp.Value = temp
			solarPanelTemperature.Set(temp)
			solarPanelVoltage.Set(msg.Value)
			continue
		}

		if msg.Dev != "temp" {
			continue
		}

		switch msg.Circuit {
		case sensors.SolarIn.Circuit:
			sensors.SolarIn.Value = msg.Value
		case sensors.SolarOut.Circuit:
			sensors.SolarOut.Value = msg.Value
		case sensors.TankUp.Circuit:
			sensors.TankUp.Value = msg.Value
		}
	}
}

/*func getSingleHomeAssistantValue(entity string) (string, error) {
	address := fmt.Sprintf("http://%s/api/states/%s", homeAssistantAddress, entity)
	authToken := fmt.Sprintf("Bearer %s", homeAssistantToken)

	req, err := http.NewRequest("GET", address, nil)
	if err != nil {
		log.Printf("Could not create request: %#v", err)
		return "", err
	}

	req.Header.Add("Authorization", authToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Could not get data from Home Assistant: %#v", err)
		return "", err
	}

	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Could not read response body: %#v", err)
		return "", err
	}

	var data types.HomeAssistantEntity
	if err := json.Unmarshal(body, &data); err != nil {
		log.Printf("Could not parse received data: %#v", err)
		return "", err
	}

	return data.State, nil
}*/

func getSingleEvokValue(dev, circuit string) (float64, error) {
	address := fmt.Sprintf("http://%s/rest/%s/%s", evokAddress, dev, circuit)

	resp, err := http.Get(address)
	if err != nil {
		log.Printf("Could not get data from EVOK: %#v", err)
		return 0, err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Could not read response body: %#v", err)
		return 0, err
	}

	var data types.EvokDevice
	if err := json.Unmarshal(body, &data); err != nil {
		log.Printf("Could not parse received data: %#v", err)
		return 0, err
	}

	return data.Value, nil
}

func setEvokSingleValue(dev, circuit string, value float64) error {
	address := fmt.Sprintf("http://%s/rest/%s/%s", evokAddress, dev, circuit)

	jsonValue, _ := json.Marshal(
		struct {
			Value float64 `json:"value"`
		}{
			value,
		},
	)

	req, err := http.NewRequest("POST", address, bytes.NewBuffer(jsonValue))
	if err != nil {
		log.Printf("Could not create request: %#v", err)
		return err
	}

	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("Could not set circuit state in EVOK: %#v", err)
		return err
	}

	defer resp.Body.Close()

	return nil
}

func onMessage(client mqtt.Client, message mqtt.Message) {
	value, err := strconv.ParseFloat(string(message.Payload()), 64)
	if err != nil {
		log.Printf("Received incorrect message payload: '%v'\n", message.Payload())
		return
	}
	switch message.Topic() {
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

func stop(reason string) {
	if circuitRunning {
		log.Println("Stopping: " + reason)

		if err := setEvokSingleValue(actuators.Pump.Dev, actuators.Pump.Circuit, 0); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := setEvokSingleValue(actuators.Switch.Dev, actuators.Switch.Circuit, 0); err != nil {
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

		if err := setEvokSingleValue(actuators.Pump.Dev, actuators.Pump.Circuit, 1); err != nil {
			log.Println(err)
			return
		}
		time.Sleep(1 * time.Second)

		if err := setEvokSingleValue(actuators.Switch.Dev, actuators.Switch.Circuit, 1); err != nil {
			log.Println(err)
			return
		}

		circuitRunning = true
		circuitRunningMetric.Set(1)
		time.Sleep(1 * time.Second)
	}
}

func calculateTemperature(voltage float64) float64 {
	// volts * (Tmax - Tmin) / Vref + Tmin
	// volts * (200 - 0) / 12 + 0
	return voltage*(200-0)/12 + 0
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

	if err := setEvokSingleValue(actuators.Flow.Dev, actuators.Flow.Circuit, value); err != nil {
		log.Println(err)
		return err
	}

	flowRate.Set(value)

	return nil
}

func setStatus(s string) {
	systemStatus.Mode = s
	systemStatus.Since = time.Now().Unix()
	if err := mqttclient.Publish(client, "solar/status", 0, false, s); err != nil {
		log.Println(err)
	}
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

func httpSolarPanelSensor(w http.ResponseWriter, r *http.Request) {
	data := struct {
		Name        string  `json:"name"`
		Temperature float64 `json:"temperature"`
		Voltage     float64 `json:"voltage"`
	}{
		Name:        "solarUp",
		Temperature: sensors.SolarUp.Value,
		Voltage:     -1,
	}

	js, err := json.Marshal(data)
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

	broker := flag.String("broker", "tcp://127.0.0.1:1883", "The full url of the MQTT server to connect to ex: tcp://127.0.0.1:1883")
	clientID := flag.String("clientid", "solar", "A clientid for the connection")
	configFile := flag.String("config", "/config.yaml", "Provide configuration file with MQTT topic mappings")
	invert := flag.Bool("invert", false, "Set this if flow regulator needs to work in 'inverted' mode (when 0V actuator is fully opened)")
	addr := flag.String("evok-address", "localhost:8080", "EVOK API address (default: localhost:8080)")
	flag.Parse()

	evokAddress = *addr

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

	// initialize sensors
	sensors.SolarUp.Value, err = getSingleEvokValue(sensors.SolarUp.Dev, sensors.SolarUp.Circuit)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	sensors.SolarIn.Value, err = getSingleEvokValue(sensors.SolarIn.Dev, sensors.SolarIn.Circuit)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	sensors.SolarOut.Value, err = getSingleEvokValue(sensors.SolarOut.Dev, sensors.SolarOut.Circuit)
	if err != nil {
		log.Fatalf("error: %v", err)
	}
	sensors.TankUp.Value, err = getSingleEvokValue(sensors.TankUp.Dev, sensors.TankUp.Circuit)
	if err != nil {
		log.Fatalf("error: %v", err)
	}

	// subscribe to configuration-related topics
	var topics []string
	topics = append(topics, settings.SolarCritical.Address, settings.SolarOn.Address, settings.SolarOff.Address, settings.TankMax.Address)
	topics = append(topics, settings.Flow.TempMin.Address, settings.Flow.TempMax.Address, settings.Flow.DutyMin.Address, settings.Flow.DutyMax.Address)
	client = mqttclient.New(*clientID, brokerURL, topics, onMessage)
	log.Printf("Connected to %s as %s and waiting for messages\n", *broker, *clientID)

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
		// Report solar panel sensor data // TODO: Rewrite to use generic funtion for all sensors
		http.HandleFunc("/sensors/panel", httpSolarPanelSensor)
		// Expose current sensors data
		http.HandleFunc("/sensors", httpSensors)
		// Expose healthcheck
		http.HandleFunc("/health", httpHealthCheck)
		err := http.ListenAndServe(":7001", nil)
		if err != nil {
			panic("HTTP Server for metrics exposition failed: " + err.Error())
		}
	}()

	go handleWebsocketMessage(evokAddress)

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

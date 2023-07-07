package evok

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type Device struct {
	Value   float64 `json:"value,omitempty" yaml:"value,omitempty"`
	Circuit string  `json:"circuit" yaml:"circuit"`
	Dev     string  `json:"dev" yaml:"dev"`
}

type Sensors struct {
	SolarUp  Device `yaml:"solarUp"`
	SolarIn  Device `yaml:"solarIn"`
	SolarOut Device `yaml:"solarOut"`
	TankUp   Device `yaml:"tankUp"`
}

type Actuators struct {
	Pump   Device `yaml:"pump"`
	Switch Device `yaml:"switch"`
	Flow   Device `yaml:"flow"`
}

var (
	evokAddress string
	sensors     *Sensors

	/*solarPanelVoltage = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "solar_panel_voltage_volts",
		Help: "Voltage reported by solar panel temperature sensor",
	})
	solarPanelTemperature = prometheus.NewGauge(prometheus.GaugeOpts{
		Name: "solar_panel_temperature_celsius",
		Help: "Temperature of solar panel",
	})*/
)

func SetAddress(address string) {
	evokAddress = address
}

func SetSensors(s *Sensors) {
	sensors = s
}

func GetSensors() *Sensors {
	return sensors
}

func ExposeSensorsOnHTTP(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(&sensors)
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

func HandleWebsocketConnection() {
	fmt.Printf("Connecting to EVOK at %s\n", evokAddress)

	conn, err := establishWebsocketConnection(evokAddress)
	if err != nil {
		panic(fmt.Sprintf("Connecting to EVOK failed: %v", err))
	}
	defer conn.Close()

	sendWebsocketFilterMessage(conn)

	processWebsocketMessages(conn)
}

func establishWebsocketConnection(address string) (net.Conn, error) {
	conn, _, _, err := ws.DefaultDialer.Dial(context.TODO(), "ws://"+address+"/ws")
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func sendWebsocketFilterMessage(conn net.Conn) {
	msg := "{\"cmd\":\"filter\", \"devices\":[\"ai\",\"temp\"]}"
	if err := wsutil.WriteClientMessage(conn, ws.OpText, []byte(msg)); err != nil {
		panic("Sending websocket message to EVOK failed: " + err.Error())
	}
}

func processWebsocketMessages(conn net.Conn) {
	var inputs []Device
	for {
		payload, err := wsutil.ReadServerText(conn)
		if err != nil {
			log.Printf("Received incorrect data: %#v", err)
			continue
		}

		if err := json.Unmarshal(payload, &inputs); err != nil {
			log.Printf("Could not parse received data: %#v", err)
			continue
		}

		parseData(inputs)
	}
}

func parseData(data []Device) {
	for _, msg := range data {
		if msg.Circuit == sensors.SolarUp.Circuit && msg.Dev == sensors.SolarUp.Dev {
			temp := calculateTemperature(msg.Value)
			sensors.SolarUp.Value = temp
			//solarPanelTemperature.Set(temp)
			//solarPanelVoltage.Set(msg.Value)
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

func calculateTemperature(voltage float64) float64 {
	return voltage*(200-0)/12 + 0
}

func InitializeSensorsValues() error {
	var err error
	sensors.SolarUp.Value, err = GetSingleValue(sensors.SolarUp.Dev, sensors.SolarUp.Circuit)
	if err != nil {
		return fmt.Errorf("failed to initialize SolarUp value: %w", err)
	}
	sensors.SolarIn.Value, err = GetSingleValue(sensors.SolarIn.Dev, sensors.SolarIn.Circuit)
	if err != nil {
		return fmt.Errorf("failed to initialize SolarIn value: %w", err)
	}
	sensors.SolarOut.Value, err = GetSingleValue(sensors.SolarOut.Dev, sensors.SolarOut.Circuit)
	if err != nil {
		return fmt.Errorf("failed to initialize SolarOut value: %w", err)
	}
	sensors.TankUp.Value, err = GetSingleValue(sensors.TankUp.Dev, sensors.TankUp.Circuit)
	if err != nil {
		return fmt.Errorf("failed to initialize TankUp value: %w", err)
	}
	return nil
}

func GetSingleValue(dev, circuit string) (float64, error) {
	address := fmt.Sprintf("http://%s/rest/%s/%s", evokAddress, dev, circuit)

	resp, err := http.Get(address)
	if err != nil {
		return 0, fmt.Errorf("failed to get data from EVOK: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("failed to read response body: %w", err)
	}

	var data Device
	if err := json.Unmarshal(body, &data); err != nil {
		return 0, fmt.Errorf("failed to parse received data: %w", err)
	}

	return data.Value, nil
}

func SetSingleValue(dev, circuit string, value float64) error {
	address := fmt.Sprintf("http://%s/json/%s/%s", evokAddress, dev, circuit)

	var stringValue string
	if dev == "ao" {
		stringValue = fmt.Sprintf("%.2f", value)
	} else {
		stringValue = fmt.Sprintf("%.0f", value)
	}

	jsonValue, _ := json.Marshal(struct {
		Value string `json:"value"`
	}{
		Value: stringValue,
	})

	req, err := http.NewRequest("POST", address, bytes.NewBuffer(jsonValue))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Add("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("failed to set circuit state in EVOK: %w", err)
	}
	defer resp.Body.Close()

	return nil
}

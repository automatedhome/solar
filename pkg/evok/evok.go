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

type Client struct {
	Sensors     Sensors
	Actuators   Actuators
	wsAddress   string
	httpAddress string
	httpClient  *http.Client
	wsConn      net.Conn
}

func NewClient(address string, sensors Sensors, actuators Actuators) *Client {
	wsAddress := "ws://" + address + "/ws"
	httpAddress := "http://" + address
	return &Client{
		Sensors:     sensors,
		Actuators:   actuators,
		wsAddress:   wsAddress,
		wsConn:      nil,
		httpAddress: httpAddress,
		httpClient:  &http.Client{},
	}
}

func (c *Client) GetSensors() *Sensors {
	return &c.Sensors
}

func (c *Client) GetActuators() *Actuators {
	return &c.Actuators
}

func (c *Client) ExposeSensorsOnHTTP(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(&c.Sensors)
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

func (c *Client) HandleWebsocketConnection() {
	log.Printf("Connecting to EVOK at %s\n", c.wsAddress)

	err := c.establishWebsocketConnection()
	if err != nil {
		log.Fatalf("Connecting to EVOK failed: %v", err)
	}
	defer c.wsConn.Close()

	c.sendWebsocketFilterMessage()

	c.processWebsocketMessages()
}

func (c *Client) establishWebsocketConnection() error {
	conn, _, _, err := ws.DefaultDialer.Dial(context.TODO(), c.wsAddress)
	if err != nil {
		return err
	}

	c.wsConn = conn

	return nil
}

func (c *Client) sendWebsocketFilterMessage() {
	msg := "{\"cmd\":\"filter\", \"devices\":[\"ai\",\"temp\"]}"
	if err := wsutil.WriteClientMessage(c.wsConn, ws.OpText, []byte(msg)); err != nil {
		panic("Sending websocket message to EVOK failed: " + err.Error())
	}
}

func (c *Client) processWebsocketMessages() {
	var inputs []Device
	for {
		payload, err := wsutil.ReadServerText(c.wsConn)
		if err != nil {
			log.Printf("Received incorrect data: %#v", err)
			continue
		}

		if err := json.Unmarshal(payload, &inputs); err != nil {
			log.Printf("Could not parse received data: %#v", err)
			continue
		}

		c.parseData(inputs)
	}
}

func (c *Client) parseData(data []Device) {
	for _, msg := range data {
		switch {
		case msg.Circuit == c.Sensors.SolarUp.Circuit && msg.Dev == c.Sensors.SolarUp.Dev:
			temp := calculateTemperature(msg.Value)
			c.Sensors.SolarUp.Value = temp
			//solarPanelTemperature.Set(temp)
			//solarPanelVoltage.Set(msg.Value)
		case msg.Dev != "temp":
			continue
		case msg.Circuit == c.Sensors.SolarIn.Circuit:
			c.Sensors.SolarIn.Value = msg.Value
		case msg.Circuit == c.Sensors.SolarOut.Circuit:
			c.Sensors.SolarOut.Value = msg.Value
		case msg.Circuit == c.Sensors.TankUp.Circuit:
			c.Sensors.TankUp.Value = msg.Value
		}
	}
}

func calculateTemperature(voltage float64) float64 {
	return voltage*(200-0)/12 + 0
}

func (c *Client) InitializeSensorsValues() error {
	var err error
	var errs []error

	err = c.updateValue(&c.Sensors.SolarUp)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateValue(&c.Sensors.SolarIn)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateValue(&c.Sensors.SolarOut)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateValue(&c.Sensors.TankUp)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d error(s) while fetching settings", len(errs))
	}

	return nil
}

func (c *Client) updateValue(obj *Device) error {
	var err error
	obj.Value, err = c.getValue(obj.Dev, obj.Circuit)
	if err != nil {
		return fmt.Errorf("failed to update value: %w", err)
	}
	return nil
}

func (c *Client) getValue(dev, circuit string) (float64, error) {
	address := fmt.Sprintf("%s/rest/%s/%s", c.httpAddress, dev, circuit)

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

func (c *Client) SetValue(dev, circuit string, value float64) error {
	address := fmt.Sprintf("%s/json/%s/%s", c.httpAddress, dev, circuit)

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

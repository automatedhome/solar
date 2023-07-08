package homeassistant

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
)

type Settings struct {
	SolarEmergency Entity       `yaml:"solarEmergency"`
	SolarCritical  Entity       `yaml:"solarCritical"`
	SolarOn        Entity       `yaml:"solarOn"`
	SolarOff       Entity       `yaml:"solarOff"`
	TankMax        Entity       `yaml:"tankMax"`
	Flow           FlowSettings `yaml:"flow"`
}

type FlowSettings struct {
	DutyMin Entity `yaml:"dutyMin"`
	TempMin Entity `yaml:"tempMin"`
	DutyMax Entity `yaml:"dutyMax"`
	TempMax Entity `yaml:"tempMax"`
}

type Entity struct {
	EntityID string  `json:"entity_id" yaml:"entity_id"`
	State    string  `json:"state,omitempty" yaml:"state,omitempty"`
	Value    float64 `json:"value,omitempty" yaml:"value,omitempty"`
}

type Client struct {
	Settings Settings
	Address  string
	Token    string
	client   *http.Client
}

func NewClient(address, token string, settings Settings) *Client {
	return &Client{
		Address:  address,
		Token:    token,
		Settings: settings,
		client:   &http.Client{},
	}
}

func (c *Client) UpdateAll() error {
	var errs []error
	var err error

	err = c.updateEntityValue(&c.Settings.SolarEmergency)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.SolarCritical)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.SolarOn)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.SolarOff)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.TankMax)
	if err != nil {
		errs = append(errs, err)
	}

	err = c.updateEntityValue(&c.Settings.Flow.DutyMin)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.Flow.DutyMax)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.Flow.TempMin)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.updateEntityValue(&c.Settings.Flow.TempMax)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d error(s) while fetching settings", len(errs))
	}

	return nil
}

func (c *Client) ExposeSettingsOnHTTP(w http.ResponseWriter, r *http.Request) {
	js, err := json.Marshal(c.Settings)
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

func (c *Client) GetSettings() Settings {
	return c.Settings
}

func (c *Client) updateEntityValue(entity *Entity) error {
	value, err := c.getSingleValue(entity.EntityID)
	if err != nil {
		log.Printf("Could not get setting for entity %s from Home Assistant: %#v", entity.EntityID, err)
		return err
	}
	entity.Value = value
	return nil
}

func (c *Client) getSingleValue(entity string) (float64, error) {
	address := fmt.Sprintf("http://%s/api/states/%s", c.Address, entity)

	req, err := http.NewRequest("GET", address, nil)
	if err != nil {
		return -1, fmt.Errorf("could not create request: %w", err)
	}

	if c.Token != "" {
		req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", c.Token))
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return -1, fmt.Errorf("could not get data from Home Assistant: %w", err)
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return -1, fmt.Errorf("could not read response body: %w", err)
	}

	var data Entity
	if err := json.Unmarshal(body, &data); err != nil {
		return -1, fmt.Errorf("could not parse received data: %w", err)
	}

	// Special case for handling boolean values
	switch data.State {
	case "on":
		return 1, nil
	case "off":
		return 0, nil
	}

	data.Value, err = strconv.ParseFloat(data.State, 64)
	if err != nil {
		return -1, fmt.Errorf("could not convert value to float64: %w", err)
	}

	return data.Value, nil
}

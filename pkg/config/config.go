package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/automatedhome/solar/pkg/evok"
	"github.com/automatedhome/solar/pkg/homeassistant"
	"gopkg.in/yaml.v2"
)

type Config struct {
	Settings  Settings
	Actuators evok.Actuators
	Sensors   evok.Sensors
}

type Settings struct {
	SolarCritical homeassistant.Entity `yaml:"solarCritical"`
	SolarOn       homeassistant.Entity `yaml:"solarOn"`
	SolarOff      homeassistant.Entity `yaml:"solarOff"`
	TankMax       homeassistant.Entity `yaml:"tankMax"`
	Flow          FlowSettings         `yaml:"flow"`
}

type FlowSettings struct {
	DutyMin homeassistant.Entity `yaml:"dutyMin"`
	TempMin homeassistant.Entity `yaml:"tempMin"`
	DutyMax homeassistant.Entity `yaml:"dutyMax"`
	TempMax homeassistant.Entity `yaml:"tempMax"`
}

func NewConfig(cfgFile string) (*Config, error) {
	log.Printf("Reading configuration from %s", cfgFile)
	data, err := ioutil.ReadFile(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("file reading error: %w", err)
	}

	var config Config
	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		return nil, fmt.Errorf("error: %w", err)
	}

	log.Printf("Reading following config from config file: %#v", config)

	return &config, nil
}

func (c *Config) ExposeSettingsOnHTTP(w http.ResponseWriter, r *http.Request) {
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

func (c *Config) ReadValuesFromHomeAssistant(hassClient *homeassistant.Client) error {
	var errs []error

	err := c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.SolarCritical)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.SolarOn)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.SolarOff)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.TankMax)
	if err != nil {
		errs = append(errs, err)
	}

	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.Flow.DutyMin)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.Flow.DutyMax)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.Flow.TempMin)
	if err != nil {
		errs = append(errs, err)
	}
	err = c.getSingleValueFromHomeAssistant(hassClient, &c.Settings.Flow.TempMax)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d error(s) while fetching settings", len(errs))
	}

	return nil
}

func (c *Config) getSingleValueFromHomeAssistant(hassClient *homeassistant.Client, entity *homeassistant.Entity) error {
	value, err := hassClient.GetSingleValue(entity.EntityID)
	if err != nil {
		log.Printf("Could not get setting for entity %s from Home Assistant: %#v", entity.EntityID, err)
		return err
	}
	entity.Value = value
	return nil
}

func (c *Config) GetSensors() *evok.Sensors {
	return &c.Sensors
}

func (c *Config) GetActuators() *evok.Actuators {
	return &c.Actuators
}

func (c *Config) GetSettings() *Settings {
	return &c.Settings
}

package config

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"

	"github.com/automatedhome/solar/pkg/homeassistant"
	types "github.com/automatedhome/solar/pkg/types"
	"gopkg.in/yaml.v2"
)

var settings types.Settings
var actuators types.Actuators
var sensors types.Sensors

func ExposeOnHTTP(w http.ResponseWriter, r *http.Request) {
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

func ReadConfigFromFile(cfg string) {
	log.Printf("Reading configuration from %s", cfg)
	data, err := ioutil.ReadFile(cfg)
	if err != nil {
		log.Fatalf("File reading error: %v", err)
		return
	}

	var config struct {
		Settings  types.Settings  `yaml:"settings"`
		Actuators types.Actuators `yaml:"actuators"`
		Sensors   types.Sensors   `yaml:"sensors"`
	}
	if err := yaml.UnmarshalStrict(data, &config); err != nil {
		log.Fatalf("error: %v", err)
	}

	log.Printf("Reading following config from config file: %#v", config)

	settings = config.Settings
	actuators = config.Actuators
	sensors = config.Sensors
}

func UpdateValuesFromHomeAssistant(hassClient *homeassistant.Client) error {
	var errs []error
	var err error

	settings.SolarCritical.Value, err = hassClient.GetSingleValue(settings.SolarCritical.EntityID)
	if err != nil {
		log.Printf("Could not get setting for solar critical temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.SolarOn.Value, err = hassClient.GetSingleValue(settings.SolarOn.EntityID)
	if err != nil {
		log.Printf("Could not get setting for solar on temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.SolarOff.Value, err = hassClient.GetSingleValue(settings.SolarOff.EntityID)
	if err != nil {
		log.Printf("Could not get setting for solar off temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.TankMax.Value, err = hassClient.GetSingleValue(settings.TankMax.EntityID)
	if err != nil {
		log.Printf("Could not get setting for tank max temperature from Home Assistant: %#v", err)
		errs = append(errs, err)
	}

	settings.Flow.DutyMin.Value, err = hassClient.GetSingleValue(settings.Flow.DutyMin.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow duty min from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.Flow.DutyMax.Value, err = hassClient.GetSingleValue(settings.Flow.DutyMax.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow duty max from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.Flow.TempMin.Value, err = hassClient.GetSingleValue(settings.Flow.TempMin.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow temp min from Home Assistant: %#v", err)
		errs = append(errs, err)
	}
	settings.Flow.TempMax.Value, err = hassClient.GetSingleValue(settings.Flow.TempMax.EntityID)
	if err != nil {
		log.Printf("Could not get setting for flow temp max from Home Assistant: %#v", err)
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("encountered %d error(s) while fetching settings", len(errs))
	}

	return nil
}

func GetSensors() *types.Sensors {
	return &sensors
}

func GetActuators() *types.Actuators {
	return &actuators
}

func GetSettings() *types.Settings {
	return &settings
}

package config

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"

	"github.com/automatedhome/solar/pkg/evok"
	"github.com/automatedhome/solar/pkg/homeassistant"
	"gopkg.in/yaml.v2"
)

var internalConfigFile = "/config.yaml"

type Config struct {
	Settings  homeassistant.Settings
	Actuators evok.Actuators
	Sensors   evok.Sensors
}

func NewConfig(cfgFile *string) (*Config, error) {
	configFilePath := internalConfigFile
	if cfgFile == nil {
		configFilePath = *cfgFile
	}

	log.Printf("Reading configuration from %s", configFilePath)

	if _, err := os.Stat(configFilePath); err != nil {
		log.Fatalf("Config file %s does not exist", configFilePath)
	}

	data, err := ioutil.ReadFile(configFilePath)
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

func (c *Config) GetSensorsConfig() *evok.Sensors {
	return &c.Sensors
}

func (c *Config) GetActuatorsConfig() *evok.Actuators {
	return &c.Actuators
}

func (c *Config) GetSettingsConfig() *homeassistant.Settings {
	return &c.Settings
}

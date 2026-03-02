package config

import (
	"fmt"
	"log"
	"strings"

	"github.com/spf13/viper"
)

const (
	configName = "config"
	fileType   = "yaml"
	configPath = "./config"
)

var C = new(Configurations)

type Configurations struct {
	Server Server
	Mysql  Mysql
	Redis  Redis
	LLM    LLM
}

type Server struct {
	Host string
	Port int
}

type Mysql struct {
	DBUser     string
	DBPassword string
	DBName     string
	DBHost     string
	DBPort     int
}

type Redis struct {
	Host     string
	Port     int
	Password string
	Db       int
}

type LLM struct {
	Provider    string
	Model       string
	Ollamamodel string
	BaseURL     string
	APIKey      string
}

func InitConfigs() {
	initViper()
	fromConfig()
}

func initViper() {
	viper.SetConfigName(configName)
	viper.SetConfigType(fileType)
	viper.AddConfigPath(configPath)
}

func fromConfig() {
	err := viper.ReadInConfig()
	if err != nil {
		log.Panic("Read configs error，reason：" + err.Error())
	}

	err = viper.Unmarshal(&C)
	if err != nil {
		log.Panic("Unmarshal error: " + err.Error())
	}
}

func GetServerAddress() string {
	return fmt.Sprintf("%s:%d", C.Server.Host, C.Server.Port)
}

func GetMySqlAddress() string {
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s", C.Mysql.DBUser, C.Mysql.DBPassword, C.Mysql.DBHost, C.Mysql.DBPort, C.Mysql.DBName)
}

func GetRedisAddress() string {
	return fmt.Sprintf("%s:%d", C.Redis.Host, C.Redis.Port)
}

func GetLLMProvider() string {
	provider := strings.TrimSpace(strings.ToLower(C.LLM.Provider))
	if provider == "" {
		return "ollama"
	}
	return provider
}

func GetLLMModel() string {
	if model := strings.TrimSpace(C.LLM.Model); model != "" {
		return model
	}
	if legacy := strings.TrimSpace(C.LLM.Ollamamodel); legacy != "" {
		return legacy
	}
	return "mistral"
}

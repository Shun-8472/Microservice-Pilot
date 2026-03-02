package mysql

import (
	"database/sql"
	"fmt"

	_ "github.com/go-sql-driver/mysql"

	"mini/config"
	"mini/internal/applied/database"
)

var (
	DatabaseConnection = new(sql.DB)
	err                error
)

type Database struct {
}

func NewConnect() database.Database {
	return &Database{}
}

func (d Database) ConnectDatabase() {
	connectionInfo := config.GetMySqlAddress()

	DatabaseConnection, err = sql.Open("mysql", connectionInfo)
	if err != nil {
		panic("failed to open database: " + err.Error())
	}

	if err = DatabaseConnection.Ping(); err != nil {
		panic(fmt.Sprintf("failed to ping database (%s): %v", connectionInfo, err))
	}
}

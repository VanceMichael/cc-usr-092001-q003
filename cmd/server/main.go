package main

import (
	"log"
	"net/http"
	"os"

	"example.com/batch-092001-q003/internal/httpapi"
	"example.com/batch-092001-q003/internal/service"
	"example.com/batch-092001-q003/internal/store"
)

func main() {
	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "data/state.json"
	}
	st, err := store.Open(dbPath)
	if err != nil {
		log.Fatalf("打开数据存储失败: %v", err)
	}
	svc := service.New(st)
	server := httpapi.New(svc)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("院内制剂传承证据链服务监听 :%s（数据目录 %s）", port, dbPath)
	if err := http.ListenAndServe("0.0.0.0:"+port, server.Handler()); err != nil {
		log.Fatal(err)
	}
}

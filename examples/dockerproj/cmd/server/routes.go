package main

import (
	"encoding/json"
	"log"
	"net/http"

	"gorm.io/gorm"

	"example.com/dockerproj/shop"
)

func routes(db *gorm.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /orders", func(w http.ResponseWriter, r *http.Request) {
		var orders []shop.Order
		if err := db.WithContext(r.Context()).Order("id").Find(&orders).Error; err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, orders)
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var order shop.Order
		if err := json.NewDecoder(r.Body).Decode(&order); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if err := db.WithContext(r.Context()).Create(&order).Error; err != nil {
			serverError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, order)
	})
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Println("write response:", err)
	}
}

func serverError(w http.ResponseWriter, err error) {
	log.Println("request failed:", err)
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

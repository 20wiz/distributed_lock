package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/redigo"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gomodule/redigo/redis"
	"golang.org/x/net/context"
)

var (
	db     *sql.DB
	rdb    *goredis.Client
	ctx    = context.Background()
	rs     *redsync.Redsync
	lock   *redsync.Mutex
	lockMu sync.Mutex
)

func initDB() {
	var err error
	dsn := "app_user:app_password@tcp(mysql:3306)/dist_lock"
	db, err = sql.Open("mysql", dsn)
	if err != nil {
		log.Fatalf("Error opening database: %v", err)
	}
	if err = db.Ping(); err != nil {
		log.Fatalf("Error connecting to the database: %v", err)
	}
}

func initRedis() {
	client := goredis.NewClient(&goredis.Options{
		Addr:        "redis:6379",
		DialTimeout: time.Second * 5,
	})
	if _, err := client.Ping(ctx).Result(); err != nil {
		log.Fatalf("Error connecting to Redis: %v", err)
	}
	rdb = client

	pool := &redis.Pool{
		MaxIdle:   3,
		MaxActive: 64,
		Dial: func() (redis.Conn, error) {
			c, err := redis.Dial("tcp", "redis:6379")
			if err != nil {
				return nil, err
			}
			return c, err
		},
	}
	rs = redsync.New(redigo.NewPool(pool))
	lock = rs.NewMutex("myLock")
}

func incrementHandler(w http.ResponseWriter, r *http.Request) {
	lockMu.Lock()
	defer lockMu.Unlock()
	if err := lock.LockContext(ctx); err != nil {
		http.Error(w, "Could not acquire lock", http.StatusInternalServerError)
		return
	}
	defer lock.UnlockContext(ctx)

	var currentValue int
	err := db.QueryRow("SELECT value FROM counter WHERE id = 1").Scan(&currentValue)
	if err != nil {
		http.Error(w, "Could not read current value", http.StatusInternalServerError)
		return
	}
	newValue := currentValue + 1
	_, err = db.Exec("UPDATE counter SET value = ? WHERE id = 1", newValue)
	if err != nil {
		http.Error(w, "Could not update value", http.StatusInternalServerError)
		return
	}
	fmt.Fprintf(w, "New value: %d", newValue)
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "healthy")
}

func statusHandler(w http.ResponseWriter, r *http.Request) {
	// Check DB
	err := db.Ping()
	dbStatus := "up"
	if err != nil {
		dbStatus = "down"
	}

	// Check Redis
	_, err = rdb.Ping(ctx).Result()
	redisStatus := "up"
	if err != nil {
		redisStatus = "down"
	}

	fmt.Fprintf(w, "Database: %s\nRedis: %s", dbStatus, redisStatus)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintf(w, "Distributed Lock Demo\nAvailable endpoints:\n/health\n/status\n/increment")
}

func main() {
	initDB()
	initRedis()

	http.HandleFunc("/", rootHandler)
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/status", statusHandler)
	http.HandleFunc("/increment", incrementHandler)

	log.Printf("Server starting on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

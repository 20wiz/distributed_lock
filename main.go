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
	// Try localhost first, fallback to container name
	hosts := []string{"localhost", "mysql"}

	for _, host := range hosts {
		dsn := fmt.Sprintf("app_user:app_password@tcp(%s:3306)/dist_lock", host)
		db, err = sql.Open("mysql", dsn)
		if err == nil {
			if err = db.Ping(); err == nil {
				log.Printf("Connected to MySQL at %s", host)
				return
			}
		}
		log.Printf("Failed to connect to MySQL at %s: %v", host, err)
	}
	log.Fatal("Could not connect to MySQL")
}

func initRedis() {
	var client *goredis.Client
	hosts := []string{"localhost", "redis"}

	for _, host := range hosts {
		client = goredis.NewClient(&goredis.Options{
			Addr:        fmt.Sprintf("%s:6379", host),
			DialTimeout: time.Second * 5,
		})
		if _, err := client.Ping(ctx).Result(); err == nil {
			log.Printf("Connected to Redis at %s", host)
			rdb = client

			pool := &redis.Pool{
				MaxIdle:   3,
				MaxActive: 64,
				Dial: func() (redis.Conn, error) {
					return redis.Dial("tcp", fmt.Sprintf("%s:6379", host))
				},
			}
			rs = redsync.New(redigo.NewPool(pool))
			lock = rs.NewMutex("myLock")
			return
		} else {
			log.Printf("Failed to connect to Redis at %s: %v", host, err)
		}
	}
	log.Fatal("Could not connect to Redis")
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
	html := `
<!DOCTYPE html>
<html>
<head>
    <title>Distributed Lock Demo</title>
    <style>
        body { font-family: Arial; margin: 40px; }
        button { 
            padding: 10px 20px;
            margin: 5px;
            cursor: pointer;
        }
        #response {
            margin-top: 20px;
            padding: 10px;
            border: 1px solid #ccc;
            min-height: 50px;
        }
    </style>
</head>
<body>
    <h1>Distributed Lock Demo</h1>
    <div>
        <button onclick="fetchEndpoint('/health')">Health Check</button>
        <button onclick="fetchEndpoint('/status')">Status</button>
        <button onclick="fetchEndpoint('/increment')">Increment</button>
    </div>
    <div id="response"></div>

    <script>
    async function fetchEndpoint(path) {
        const response = document.getElementById('response');
        try {
            const res = await fetch(path);
            const text = await res.text();
            response.innerHTML = ` + "`<pre>${text}</pre>`" + `;
        } catch (err) {
            response.innerHTML = ` + "`Error: ${err.message}`" + `;
        }
    }
    </script>
</body>
</html>
`
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
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

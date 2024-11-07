package main

import (
	"database/sql"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/redigo"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gomodule/redigo/redis"
	"golang.org/x/net/context"
)

// Add port to environment variables
var (
	db          *sql.DB
	rdb         *goredis.Client
	ctx         = context.Background()
	rs          *redsync.Redsync
	lock        *redsync.Mutex
	lockMu      sync.Mutex
	isPaused    bool
	pauseLock   sync.Mutex
	containerID string
	appPort     string
	localPort   string
	workers     = 4
	wg          sync.WaitGroup
)

func init() {
	containerID = os.Getenv("CONTAINER_ID")
	if containerID == "" {
		containerID = "unknown"
	}
	appPort = os.Getenv("APP_PORT")
	if appPort == "" {
		appPort = "8080"
	}
	localPort = "8080"
}

func initDB() {
	var err error
	// Try localhost first, fallback to container name
	hosts := []string{"localhost", "mysql"}

	for _, host := range hosts {
		dsn := fmt.Sprintf("app_user:app_password@tcp(%s:3306)/dist", host)
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
	// Try to get worker ID from URL param first, then header
	workerID := r.URL.Query().Get("worker")
	if workerID == "" {
		workerID = r.Header.Get("X-Worker-ID")
	}
	if workerID == "" {
		workerID = "unknown"
	}

	updatedBy := fmt.Sprintf("%s_Worker%s", containerID, workerID)
	log.Printf("[%s] Increment request received from %s", containerID, updatedBy)

	pauseLock.Lock()
	if isPaused {
		pauseLock.Unlock()
		log.Printf("[%s] Service is paused", containerID)
		http.Error(w, "Service is paused", http.StatusServiceUnavailable)
		return
	}
	pauseLock.Unlock()

	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		mutex := rs.NewMutex("counter_lock",
			redsync.WithExpiry(10*time.Second),
			redsync.WithTries(3),
			redsync.WithRetryDelay(200*time.Millisecond),
		)

		if err := mutex.Lock(); err != nil {
			if i == maxRetries-1 {
				log.Printf("[%s] Failed to acquire lock after %d attempts: %v", containerID, maxRetries, err)
				http.Error(w, "Too many requests, please try again later", http.StatusTooManyRequests)
				return
			}
			waitTime := time.Duration(math.Pow(2, float64(i))) * 100 * time.Millisecond
			log.Printf("[%s] Retry %d: waiting %v before next attempt", containerID, i+1, waitTime)
			time.Sleep(waitTime)
			continue
		}

		defer mutex.Unlock()
		log.Printf("[%s] Lock acquired", containerID)

		// Artificial delay
		time.Sleep(time.Duration(200+rand.Intn(200)) * time.Millisecond)

		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			http.Error(w, "Could not start transaction", http.StatusInternalServerError)
			return
		}
		defer tx.Rollback()

		// Get latest value
		var currentValue int
		err = tx.QueryRowContext(ctx, "SELECT count FROM counter ORDER BY id DESC LIMIT 1").Scan(&currentValue)
		if err != nil {
			http.Error(w, "Could not read current count", http.StatusInternalServerError)
			return
		}

		// Insert new row with incremented value
		newValue := currentValue + 1
		_, err = tx.ExecContext(ctx,
			"INSERT INTO counter (count, last_updated_by) VALUES (?, ?)",
			newValue, updatedBy)
		if err != nil {
			http.Error(w, "Could not insert new count", http.StatusInternalServerError)
			return
		}

		if err := tx.Commit(); err != nil {
			http.Error(w, "Could not commit transaction", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"update_by": "%s_%s", "previous_count": %d, "new_count": %d}`,
			containerID, workerID, currentValue, newValue)
		return
	}
}

func pauseHandler(w http.ResponseWriter, r *http.Request) {
	pauseLock.Lock()
	defer pauseLock.Unlock()
	isPaused = !isPaused
	fmt.Fprintf(w, `{"paused": %v}`, isPaused)
}

func startIncrementLoop() {
	ticker := time.NewTicker(1 * time.Second)

	// Start worker pool
	for i := 0; i < workers; i++ {
		wg.Add(1)
		workerID := i + 1
		go func(id int) {
			defer wg.Done()
			for range ticker.C {
				// Ensure worker ID is always passed
				url := fmt.Sprintf("http://localhost:%s/increment?worker=%d", localPort, id)
				log.Printf("[%s-Worker%d] Requesting increment", containerID, id)

				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					log.Printf("[%s-Worker%d] Error creating request: %v", containerID, id, err)
					continue
				}

				// Add worker ID to request header as backup
				req.Header.Set("X-Worker-ID", fmt.Sprintf("%d", id))

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					log.Printf("[%s-Worker%d] Error: %v", containerID, id, err)
					continue
				}
				if resp != nil {
					log.Printf("[%s-Worker%d] Response: %s", containerID, id, resp.Status)
					resp.Body.Close()
				}
			}
		}(workerID)
	}
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

func lockStatusHandler(w http.ResponseWriter, r *http.Request) {
	val, err := rdb.Get(ctx, "counter_lock").Result()
	if err != nil {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "Lock is free")
		return
	}

	ttl, _ := rdb.TTL(ctx, "counter_lock").Result()
	fmt.Fprintf(w, "Lock is held. TTL: %v\nValue: %s", ttl, val)
}

func getCurrentCounterHandler(w http.ResponseWriter, r *http.Request) {
	var count int
	var updatedBy string
	// var updatedAt time.Time
	var createdAt []uint8

	err := db.QueryRow("SELECT count, last_updated_by, created_at FROM counter ORDER BY id DESC LIMIT 1").
		Scan(&count, &updatedBy, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("No counter records found")
			// Return initial state if no records
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"count": 0, "updated_by": "none", "updated_at": "%s"}`,
				time.Now().Format(time.RFC3339))
			return
		}
		log.Printf("Error reading counter count: %v", err)
		http.Error(w, "Could not read count", http.StatusInternalServerError)
		return
	}

	// Convert createdAt to time.Time
	createdAtTime, err := time.Parse("2006-01-02 15:04:05", string(createdAt))
	if err != nil {
		log.Printf("Error parsing created_at: %v", err)
		http.Error(w, "Could not parse created_at", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"count": %d, "updated_by": "%s", "updated_at": "%s"}`,
		count, updatedBy, createdAtTime.Format(time.RFC3339))
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	html := `
<!DOCTYPE html>
<html>
<head>
    <title>Distributed Lock Demo</title>
    <style>
        body { font-family: Arial; margin: 40px; }
        .counter {
            font-size: 48px;
            margin: 20px 0;
        }
        .container {
            text-align: center;
        }
        .info {
            color: #666;
            font-size: 14px;
        }
        .error {
            color: #ff0000;
            margin: 10px 0;
        }
    </style>
</head>
<body>
    <div class="container">
        <h1>Counter Value</h1>
        <div class="counter" id="counterValue">0</div>
        <div class="info" id="lastUpdated"></div>
        <div class="error" id="errorMsg"></div>
    </div>

    <script>
    async function updateCounter() {
        try {
            const response = await fetch('/counter');
            const data = await response.json();
            document.getElementById('counterValue').textContent = data.count;
            document.getElementById('lastUpdated').textContent = 
                'Last updated by: ' + data.updated_by + 
                ' at ' + new Date(data.updated_at).toLocaleTimeString();
            document.getElementById('errorMsg').textContent = '';
        } catch (err) {
            document.getElementById('errorMsg').textContent = 'Error: ' + err.message;
        }
    }

    // Update every second
    setInterval(updateCounter, 1000);
    // Initial update
    updateCounter();
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
	http.HandleFunc("/lock-status", lockStatusHandler)
	http.HandleFunc("/pause", pauseHandler)
	http.HandleFunc("/counter", getCurrentCounterHandler)
	startIncrementLoop()

	log.Printf("Server starting on :" + localPort)
	log.Fatal(http.ListenAndServe(":"+localPort, nil))
}

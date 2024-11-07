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

	"github.com/gin-gonic/gin"
	goredis "github.com/go-redis/redis/v8"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/redigo"
	_ "github.com/go-sql-driver/mysql"
	"github.com/gomodule/redigo/redis"
	"golang.org/x/net/context"
)

var (
	db          *sql.DB
	rdb         *goredis.Client
	ctx         = context.Background()
	rs          *redsync.Redsync
	lock        *redsync.Mutex
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
	localPort = os.Getenv("LOCAL_PORT")
	if localPort == "" {
		localPort = "8080"
	}
}

func initDB() {
	var err error
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

// LockHandler attempts to acquire the distributed lock
func lockHandler(c *gin.Context) {
	if err := lock.Lock(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "failed to acquire lock", "error": err.Error()})
		return
	}

	// Set a Redis key to indicate lock status
	if err := rdb.SetNX(ctx, "lock_status", "locked", 0).Err(); err != nil {
		log.Printf("Failed to set lock_status: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{"status": "locked"})
}

// UnlockHandler releases the distributed lock
func unlockHandler(c *gin.Context) {
	if ok, err := lock.Unlock(); !ok || err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"status": "failed to release lock", "error": err.Error()})
		return
	}

	// Remove the Redis key indicating lock status
	if err := rdb.Del(ctx, "lock_status").Err(); err != nil {
		log.Printf("Failed to delete lock_status: %v", err)
	}

	c.JSON(http.StatusOK, gin.H{"status": "unlocked"})
}

// LockStatusHandler checks the current lock status
func lockStatusHandler(c *gin.Context) {
	status, err := rdb.Get(ctx, "lock_status").Result()
	if err == goredis.Nil {
		status = "unlocked"
	} else if err != nil {
		log.Printf("Failed to get lock_status: %v", err)
		status = "unknown"
	}

	c.JSON(http.StatusOK, gin.H{"status": status})
}

func incrementHandler(c *gin.Context) {
	// Check if the lock is held
	status, err := rdb.Get(ctx, "lock_status").Result()
	if err == nil && status == "locked" {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Service is locked"})
		return
	}

	workerID := c.Query("worker")
	if workerID == "" {
		workerID = c.GetHeader("X-Worker-ID")
	}
	if workerID == "" {
		workerID = "unknown"
	}

	updatedBy := fmt.Sprintf("%s_Worker%s", containerID, workerID)
	log.Printf("[%s] Increment request received from %s", containerID, updatedBy)

	pauseLock.Lock()
	if isPaused {
		pauseLock.Unlock()
		log.Printf("[%s-Worker%s] Service is paused", containerID, workerID)
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "Service is paused"})
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
				log.Printf("[%s-Worker%s] Failed to acquire lock after %d attempts: %v",
					containerID, workerID, maxRetries, err)
				c.JSON(http.StatusTooManyRequests, gin.H{"error": "Too many requests, please try again later"})
				return
			}
			waitTime := time.Duration(math.Pow(2, float64(i))) * 100 * time.Millisecond
			log.Printf("[%s-Worker%s] Retry %d: waiting %v before next attempt",
				containerID, workerID, i+1, waitTime)
			time.Sleep(waitTime)
			continue
		}

		defer mutex.Unlock()
		log.Printf("[%s-Worker%s] Lock acquired", containerID, workerID)

		// Artificial delay
		time.Sleep(time.Duration(100+rand.Intn(100)) * time.Millisecond)

		tx, err := db.BeginTx(ctx, nil) // db transaction also locks the row, not required for this example
		if err != nil {
			log.Printf("[%s-Worker%s] Could not start transaction: %v", containerID, workerID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not start transaction"})
			return
		}
		defer tx.Rollback()

		// Get latest value
		var currentValue int
		err = tx.QueryRowContext(ctx, "SELECT counter_value FROM counter ORDER BY id DESC LIMIT 1").Scan(&currentValue)
		if err != nil {
			log.Printf("[%s-Worker%s] Could not read current value: %v", containerID, workerID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not read current value"})
			return
		}

		// Insert new row with incremented value
		newValue := currentValue + 1
		_, err = tx.ExecContext(ctx,
			"INSERT INTO counter (counter_value, last_updated_by) VALUES (?, ?)",
			newValue, updatedBy)
		if err != nil {
			log.Printf("[%s-Worker%s] Could not insert new value: %v", containerID, workerID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not insert new value"})
			return
		}

		if err := tx.Commit(); err != nil {
			log.Printf("[%s-Worker%s] Could not commit transaction: %v", containerID, workerID, err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not commit transaction"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"container":      containerID,
			"worker":         workerID,
			"previous_value": currentValue,
			"new_value":      newValue,
		})
		return
	}
}

func getCurrentCounterHandler(c *gin.Context) {
	var counterValue int
	var updatedBy string
	var createdAt []uint8

	err := db.QueryRow("SELECT counter_value, last_updated_by, created_at FROM counter ORDER BY id DESC LIMIT 1").
		Scan(&counterValue, &updatedBy, &createdAt)

	if err != nil {
		if err == sql.ErrNoRows {
			log.Printf("No counter records found")
			// Return initial state if no records
			c.JSON(http.StatusOK, gin.H{
				"counter_value": 0,
				"updated_by":    "none",
				"updated_at":    time.Now().Format(time.RFC3339),
			})
			return
		}
		log.Printf("Error reading counter value: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not read counter value"})
		return
	}

	// Convert createdAt to time.Time
	createdAtTime, err := time.Parse("2006-01-02 15:04:05", string(createdAt))
	if err != nil {
		log.Printf("Error parsing created_at: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Could not parse created_at"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"counter_value": counterValue,
		"updated_by":    updatedBy,
		"updated_at":    createdAtTime.Format(time.RFC3339),
	})
}

func pauseHandler(c *gin.Context) {
	pauseLock.Lock()
	defer pauseLock.Unlock()
	isPaused = !isPaused
	c.JSON(http.StatusOK, gin.H{"paused": isPaused})
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

func main() {
	initDB()
	initRedis()

	r := gin.Default()
	r.LoadHTMLFiles("static/index.html")
	r.Static("/static", "./static")

	r.GET("/", func(c *gin.Context) {
		c.HTML(http.StatusOK, "index.html", nil)
	})
	// r.GET("/health", healthHandler)
	// r.GET("/status", statusHandler)
	r.GET("/increment", incrementHandler)
	r.GET("/lock-status", lockStatusHandler)
	r.GET("/lock", lockHandler)
	r.GET("/unlock", unlockHandler)
	r.GET("/counter", getCurrentCounterHandler)
	r.GET("/pause", pauseHandler)

	startIncrementLoop()

	log.Printf("Server starting on :" + localPort)
	if err := r.Run(":" + localPort); err != nil {
		log.Fatalf("Failed to run server: %v", err)
	}
}

# Distributed Lock Demo

This project demonstrates the use of distributed locks with Redis and MySQL in a Go application. It uses the `redsync` library for distributed locking.

## Project Structure


## Prerequisites

- Docker
- Docker Compose

## Setup

1. Clone the repository:

    ```sh
    git clone https://github.com/20wiz/distributed_lock.git
    cd distributed_lock
    ```

2. Build and run the Docker containers:

    ```sh
    docker-compose up --build
    ```

3. The application will be available at `http://localhost:8080`.

## Endpoints

- `GET /increment`: Increments a counter in the MySQL database using a distributed lock.

## Code Overview

### `main.go`

- `initDB()`: Initializes the MySQL database connection.
- `initRedis()`: Initializes the Redis client and sets up the `redsync` distributed lock.
- `incrementHandler(w http.ResponseWriter, r *http.Request)`: Handles the `/increment` endpoint, acquiring a distributed lock before incrementing the counter in the database.

### `docker-compose.yml`

Defines the services for the application, including the Go application, MySQL, and Redis.

### `Dockerfile`

Specifies the Docker image build instructions for the Go application.

### `init.sql`

Contains the SQL script to initialize the MySQL database.

## Dependencies

- `github.com/go-redis/redis/v8`
- `github.com/go-redsync/redsync/v4`
- `github.com/go-sql-driver/mysql`
- `github.com/gomodule/redigo/redis`
- `golang.org/x/net/context`

## License

This project is licensed under the MIT License. See the [LICENSE](LICENSE) file for details.
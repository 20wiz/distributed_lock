# Distributed Lock Demo

A Go-based web application demonstrating the use of Redis-based distributed locks using Redsync. The application features a counter that can be incremented concurrently by multiple instances while ensuring consistency through distributed locking.

## Features

- **Distributed Locking:** Ensure that only one instance can modify the counter at a time using Redis and Redsync.
- **Web Interface:** Simple frontend with buttons to lock/unlock the counter and display its current status.
- **Worker Simulation:** Simulated workers that periodically increment the counter.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Setup and Installation](#setup-and-installation)
- [Running the Application](#running-the-application)
- [Endpoints](#endpoints)
- [Code Overview](#code-overview)
- [Frontend](#frontend)
- [Testing](#testing)
- [Dependencies](#dependencies)
- [License](#license)

## Prerequisites

- [Docker](https://www.docker.com/get-started) installed on your machine.
- [Docker Compose](https://docs.docker.com/compose/install/) installed.

## Setup and Installation

1. **Clone the Repository:**

   ```bash
   git clone https://github.com/20wiz/distributed_lock.git
   cd distributed_lock
   ```

2. Build and run the Docker containers:

    ```sh
    docker-compose up --build
    ```

3. The application will be available at `http://localhost:8080` and `http://localhost:8081` .

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
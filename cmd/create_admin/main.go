package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"

	"atoman/internal/service"

	"github.com/joho/godotenv"
	"golang.org/x/term"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func main() {
	// Load env: prefer .env, fallback to .env.dev
	if err := godotenv.Load(".env"); err != nil {
		if err2 := godotenv.Load(".env.dev"); err2 != nil {
			log.Println("No .env file found, using system environment variables")
		} else {
			log.Println("Loaded .env.dev")
		}
	} else {
		log.Println("Loaded .env")
	}

	dbType := os.Getenv("DATABASE_TYPE")
	dbURL := os.Getenv("DATABASE_URL")
	if dbType == "" || dbURL == "" {
		log.Fatal("DATABASE_TYPE and DATABASE_URL must be set")
	}

	var dialector gorm.Dialector
	switch dbType {
	case "postgres", "postgresql":
		dialector = postgres.Open(dbURL)
	default:
		log.Fatalf("Unsupported DATABASE_TYPE: %s (expected: postgres)", dbType)
	}

	db, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		log.Fatalf("Failed to connect to database: %v", err)
	}

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Println("Warning: create_admin is deprecated. Use init_owner with OWNER_USERNAME / OWNER_EMAIL / OWNER_PASSWORD for stable deployment bootstrap.")

	input, err := collectAdminInput(scanner, int(os.Stdin.Fd()), readPasswordFromTerminal)
	if err != nil {
		log.Fatalf("Failed to read password: %v", err)
	}

	if len(input.password) < 6 {
		log.Fatal("Password must be at least 6 characters")
	}

	user, created, err := service.NewOwnerBootstrapService(db).EnsureOwner(service.OwnerBootstrapInput{
		Username: input.username,
		Email:    input.email,
		Password: input.password,
	})
	if err != nil {
		log.Fatalf("Failed to ensure owner user: %v", err)
	}

	if created {
		fmt.Printf("Owner user '%s' created successfully.\n", user.Username)
		return
	}
	fmt.Printf("Owner user '%s' updated successfully.\n", user.Username)
}

func prompt(scanner *bufio.Scanner, label string) string {
	fmt.Print(label)
	scanner.Scan()
	return strings.TrimSpace(scanner.Text())
}

type adminInput struct {
	username string
	email    string
	password string
}

func collectAdminInput(scanner *bufio.Scanner, passwordFD int, readPassword func(int) (string, error)) (adminInput, error) {
	input := adminInput{
		username: prompt(scanner, "Username: "),
		email:    prompt(scanner, "Email: "),
	}

	fmt.Print("Password: ")
	password, err := readPassword(passwordFD)
	fmt.Println()
	if err != nil {
		return adminInput{}, err
	}
	input.password = password
	return input, nil
}

func readPasswordFromTerminal(fd int) (string, error) {
	if !term.IsTerminal(fd) {
		return "", errors.New("password input requires an interactive terminal; for non-interactive bootstrap use init_owner with OWNER_USERNAME, OWNER_EMAIL, and OWNER_PASSWORD")
	}

	password, err := term.ReadPassword(fd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(password)), nil
}

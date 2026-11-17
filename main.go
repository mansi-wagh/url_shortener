package main

import (
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// database strcture
type URL struct {
	ID           string    `json:"id"`
	OriginalURL  string    `json:"original_url"`
	ShortURL     string    `json:"short_url"`
	CreationDate time.Time `json:"creation_date"`
	ExpiryDate   time.Time `json:"expiry_date"`
}

// longest expiry a user is allowed to pick (24 hours)
const maxExpiryMinutes = 24 * 60

// longest original url we accept
const maxURLLength = 2000

// each visitor (ip address) may create this many links per window
const (
	rateLimit  = 10
	rateWindow = time.Minute
)

var urlDB = make(map[string]URL)

// many users hit the server at the same time and a Go map is not safe for that
var mu sync.RWMutex

// when each ip created its recent links, used for the rate limit
var (
	rateMu sync.Mutex
	hits   = make(map[string][]time.Time)
)

// a custom word can use letters, numbers, - and _ (3 to 30 characters)
var aliasPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{3,30}$`)

func generateShortURL(originalURL string) string {
	hasher := md5.New()

	// Convert URL to bytes and calculate its hash
	hasher.Write([]byte(originalURL))

	// Get the hash as bytes
	data := hasher.Sum(nil)
	// Convert hash bytes into a hexadecimal string
	hash := hex.EncodeToString(data)
	// Return the first 8 characters
	shortURL := fmt.Sprintf("short.ly/%s", hash[:8])
	return shortURL
}

func createURL(originalURL string, expiryMinutes int, alias string) (URL, error) {
	// the user's own word if they gave one, otherwise the generated hash
	id := alias
	if id == "" {
		// adding the time means two users shortening the same link get different ids
		id = generateShortURL(originalURL + time.Now().String())
		id = id[len("short.ly/"):]
	}

	now := time.Now()
	record := URL{
		ID:           id,
		OriginalURL:  originalURL,
		ShortURL:     "/shortly/" + id, // full link is built per request in shortURLHandler
		CreationDate: now,
		ExpiryDate:   now.Add(time.Duration(expiryMinutes) * time.Minute),
	}

	// check and save under one lock so two users cannot grab the same word
	mu.Lock()
	defer mu.Unlock()
	if old, taken := urlDB[id]; taken && now.Before(old.ExpiryDate) {
		return URL{}, fmt.Errorf("alias already taken")
	}
	urlDB[id] = record

	return record, nil
}

// allowCreate lets an ip create up to rateLimit links per rateWindow
func allowCreate(ip string) bool {
	now := time.Now()
	rateMu.Lock()
	defer rateMu.Unlock()

	// keep only the hits that are still inside the window
	recent := hits[ip][:0]
	for _, t := range hits[ip] {
		if now.Sub(t) < rateWindow {
			recent = append(recent, t)
		}
	}
	if len(recent) >= rateLimit {
		hits[ip] = recent
		return false
	}
	hits[ip] = append(recent, now)
	return true
}

// clientIP uses X-Forwarded-For when a host proxy (like Render) sits in front of us
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// cleanupLoop runs forever: every minute it removes expired links and old rate-limit entries
func cleanupLoop() {
	for range time.Tick(time.Minute) {
		now := time.Now()

		mu.Lock()
		for id, record := range urlDB {
			if now.After(record.ExpiryDate) {
				delete(urlDB, id)
			}
		}
		mu.Unlock()

		rateMu.Lock()
		for ip, times := range hits {
			if len(times) == 0 || now.Sub(times[len(times)-1]) >= rateWindow {
				delete(hits, ip)
			}
		}
		rateMu.Unlock()
	}
}

func getOriginalURL(id string) (URL, error) {
	mu.RLock()
	url, ok := urlDB[id]
	mu.RUnlock()
	if !ok {
		return URL{}, fmt.Errorf("URL not found")
	}
	return url, nil
}

func handler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	// serve the web page where users paste their long url
	http.ServeFile(w, r, "index.html")
}

// baseURL is the address the visitor used, e.g. http://localhost:3000 or https://myapp.onrender.com
func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func shortURLHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed",
			http.StatusMethodNotAllowed)
		return
	}

	if !allowCreate(clientIP(r)) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "Too many links created. Please wait a minute and try again.",
			http.StatusTooManyRequests)
		return
	}

	// a url (max 2000 chars) plus the other fields fits easily in 8 KB
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)

	var data struct {
		URL           string `json:"url"`
		ExpiryMinutes int    `json:"expiry_minutes"`
		Alias         string `json:"alias"`
	}

	if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if data.URL == "" {
		http.Error(w, "URL is required",
			http.StatusBadRequest)
		return
	}

	if len(data.URL) > maxURLLength {
		http.Error(w, fmt.Sprintf("URL is too long (maximum %d characters)", maxURLLength),
			http.StatusBadRequest)
		return
	}

	// only real web links, so nobody can shorten things like javascript:...
	parsed, err := url.ParseRequestURI(data.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		http.Error(w, "Enter a valid http:// or https:// URL", http.StatusBadRequest)
		return
	}

	if data.ExpiryMinutes < 1 || data.ExpiryMinutes > maxExpiryMinutes {
		http.Error(w, fmt.Sprintf("Expiry must be between 1 and %d minutes", maxExpiryMinutes),
			http.StatusBadRequest)
		return
	}

	if data.Alias != "" && !aliasPattern.MatchString(data.Alias) {
		http.Error(w, "Custom word must be 3-30 letters, numbers, - or _", http.StatusBadRequest)
		return
	}

	record, err := createURL(data.URL, data.ExpiryMinutes, data.Alias)
	if err != nil {
		http.Error(w, "That word is already taken, try another", http.StatusConflict)
		return
	}

	response := struct {
		ShortURL   string    `json:"short_url"`
		ExpiryDate time.Time `json:"expiry_date"`
	}{
		ShortURL:   baseURL(r) + record.ShortURL,
		ExpiryDate: record.ExpiryDate,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

func redirectHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed",
			http.StatusMethodNotAllowed)
		return
	}

	id := r.URL.Path[len("/shortly/"):]

	url, err := getOriginalURL(id)
	if err != nil {
		http.Error(w, "URL not found", http.StatusNotFound)
		return
	}

	// expired links are removed and answer 410 Gone
	if time.Now().After(url.ExpiryDate) {
		mu.Lock()
		delete(urlDB, id)
		mu.Unlock()
		http.Error(w, "This link has expired", http.StatusGone)
		return
	}

	http.Redirect(w, r, url.OriginalURL, http.StatusFound)
}

func main() {
	fmt.Println("starting url shortener service")
	// originalURL := "https://www.google.com"
	// shortURL := generateShortURL(originalURL)
	// fmt.Println("original URL:", originalURL)
	// fmt.Println("short URL:", shortURL)
	//handler function to handle the request to create a short URL
	go cleanupLoop()
	http.HandleFunc("/", handler)
	http.HandleFunc("/shorten", shortURLHandler)
	http.HandleFunc("/shortly/", redirectHandler)
	// hosting platforms give the port in PORT; default to 3000 on your own machine
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	fmt.Println("starting server on port", port)
	err := http.ListenAndServe(":"+port, nil)
	if err != nil {
		fmt.Println("Error starting server:", err)
	}
}

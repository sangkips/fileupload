package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/joho/godotenv"
)

const (
	maxFileSize = 10 << 20 // 10 MB
)

type PinataResponse struct {
	IpfsHash  string `json:"IpfsHash"`
	PinSize   int    `json:"PinSize"`
	Timestamp string `json:"Timestamp"`
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func main() {
	// Load .env file
	err := godotenv.Load(".env")
	if err != nil {
		log.Fatal("Error loading .env file")
	}

	// http.HandleFunc("/upload", handleUpload)
	http.Handle("/upload", corsMiddleware(http.HandlerFunc(handleUpload)))
	fmt.Println("Server is running on http://localhost:9000")
	log.Fatal(http.ListenAndServe(":9000", nil))
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, pinata_api_key, pinata_secret_api_key")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		sendErrorResponse(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := r.ParseMultipartForm(maxFileSize)
	if err != nil {
		sendErrorResponse(w, "Failed to parse multipart form: "+err.Error(), http.StatusBadRequest)
		return
	}

	files := r.MultipartForm.File["files"]
	if len(files) == 0 {
		sendErrorResponse(w, "No files were uploaded", http.StatusBadRequest)
		return
	}

	responses := make([]PinataResponse, 0, len(files))
	errors := make([]string, 0)

	var wg sync.WaitGroup
	var mu sync.Mutex

	for _, fileHeader := range files {
		wg.Add(1)
		go func(fh *multipart.FileHeader) {
			defer wg.Done()

			response, err := uploadFileToPinata(fh)
			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				errors = append(errors, fmt.Sprintf("Error uploading %s: %v", fh.Filename, err))
			} else {
				responses = append(responses, response)
			}
		}(fileHeader)
	}

	wg.Wait()

	result := struct {
		SuccessfulUploads []PinataResponse `json:"successful_uploads"`
		Errors            []string         `json:"errors,omitempty"`
	}{
		SuccessfulUploads: responses,
		Errors:            errors,
	}

	w.Header().Set("Content-Type", "application/json")
	if len(errors) > 0 {
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	json.NewEncoder(w).Encode(result)
}

func uploadFileToPinata(fileHeader *multipart.FileHeader) (PinataResponse, error) {
	file, err := fileHeader.Open()
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	var requestBody bytes.Buffer
	writer := multipart.NewWriter(&requestBody)

	part, err := writer.CreateFormFile("file", filepath.Base(fileHeader.Filename))
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to create form file: %w", err)
	}

	_, err = io.Copy(part, file)
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to copy file content: %w", err)
	}

	err = writer.Close()
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to close multipart writer: %w", err)
	}

	// Load environment variables
	pinataAPIKey := os.Getenv("PINATA_API_KEY")
	pinataAPISecret := os.Getenv("PINATA_API_SECRET")
	pinataAPIURL := os.Getenv("PINATA_API_URL")

	req, err := http.NewRequest("POST", pinataAPIURL, &requestBody)
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("pinata_api_key", pinataAPIKey)
	req.Header.Set("pinata_secret_api_key", pinataAPISecret)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return PinataResponse{}, fmt.Errorf("pinata API returned non-OK status: %s", resp.Status)
	}

	var pinataResp PinataResponse
	err = json.NewDecoder(resp.Body).Decode(&pinataResp)
	if err != nil {
		return PinataResponse{}, fmt.Errorf("failed to decode Pinata response: %w", err)
	}

	return pinataResp, nil
}

func sendErrorResponse(w http.ResponseWriter, message string, statusCode int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(ErrorResponse{Error: message})
}

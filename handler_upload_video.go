package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

type FFProbeResult struct {
	Streams []Stream `json:"streams"`
}

type Stream struct {
	Index     int    `json:"index"`
	CodecName string `json:"codec_name"`
	CodecType string `json:"codec_type"`
	Width     int    `json:"width,omitempty"`  // omitempty pois áudio não tem largura
	Height    int    `json:"height,omitempty"` // omitempty pois áudio não tem altura
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	const maxMemory = 1 << 30
	r.Body = http.MaxBytesReader(w, r.Body, maxMemory)
	log.Printf("TESTE do reader: %+v\n", r.Body)

	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid ID", err)
		return
	}

	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't validate JWT", err)
		return
	}

	log.Print(videoID, userID)

	// retrieving metadata
	metadata, err := cfg.db.GetVideo(videoID)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Unable to retrieve video", err)
		return
	}
	if metadata.UserID != userID {
		respondWithError(w, http.StatusUnauthorized, "User is not the owner of the vider", err)
		return
	}

	// parsing the video from the request
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Unable to parse from file", err)
		return
	}

	mediaType := header.Header.Get("Content-Type")

	fileExt := strings.Split(mediaType, "/")[1]
	if mediaType != "video/mp4" {
		respondWithError(w, http.StatusBadRequest, "invalid file type", err)
		return
	}

	key := make([]byte, 32)
	rand.Read(key)
	filename := fmt.Sprintf("%s.%s", base64.RawURLEncoding.EncodeToString(key), fileExt)
	// path := filepath.Join(cfg.assetsRoot, filename)
	createdFile, err := os.CreateTemp("", "tubely-video-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error writing video to disk.", err)
		return
	}
	// log.Printf("%+v, path: %+v", createdFile, path)
	_, err = io.Copy(createdFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying video to temp file", err)
		return
	}
	aspectRatio, err := getVideoAspectRatio(createdFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error retrieving data from the video", err)
		return
	}
	print(aspectRatio)

	defer file.Close()
	defer createdFile.Close()
	defer os.Remove(createdFile.Name())

	// uploading video to s3

	createdFile.Seek(0, io.SeekStart) // here go uses a syscall named seek in order to get the pointers of the file back to the beginning of it, so we can read the file again and upload it to the bucket

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         aws.String(fmt.Sprintf("%s/%s", aspectRatio, filename)),
		Body:        createdFile,
		ContentType: aws.String("video/mp4"),
	})

	s3VideoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s/%s", cfg.s3Bucket, cfg.s3Region, aspectRatio, filename)
	metadata.VideoURL = &s3VideoURL
	err = cfg.db.UpdateVideo(metadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to upload video", err)
		return
	}
	respondWithJSON(w, http.StatusOK, metadata)
}

func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error", "-print_format", "json", "-show_streams", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	if err != nil {
		log.Printf("Error running ffmpeg: %v\n", err)
		return "", err
	}

	var result FFProbeResult
	err = json.Unmarshal(out.Bytes(), &result)
	if err != nil {
		log.Printf("Error unmarshaling video data: %v", err)
		return "", err
	}
	width := result.Streams[0].Width
	height := result.Streams[0].Height
	ratio := checkRatio(width, height)

	return ratio, nil
}

func checkRatio(width, height int) string {
	if width == 0 || height == 0 {
		return "cannot determine ratio with zero dimensions"
	}

	ratio := float64(width) / float64(height)
	ratio16x9 := 16.0 / 9.0 // aprox 1.7777...
	ratio9x16 := 9.0 / 16.0 // 0.5625
	tolerance := 0.1
	if math.Abs(ratio-float64(ratio16x9)) < tolerance {
		return "landscape"
	}
	if math.Abs(ratio-float64(ratio9x16)) < tolerance {
		return "portrait"
	}
	return "other"
}

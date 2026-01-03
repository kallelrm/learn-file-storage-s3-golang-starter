package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

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
	path := filepath.Join(cfg.assetsRoot, filename)
	createdFile, err := os.CreateTemp("", "tubely-video-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error writing video to disk.", err)
		return
	}
	log.Printf("%+v, path: %+v", createdFile, path)
	_, err = io.Copy(createdFile, file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error copying video to temp file", err)
		return
	}

	defer file.Close()
	defer createdFile.Close()
	defer os.Remove(createdFile.Name())

	// uploading video to s3

	createdFile.Seek(0, io.SeekStart) // here go uses a syscall named seek in order to get the pointers of the file back to the beginning of it, so we can read the file again and upload it to the bucket

	_, err = cfg.s3Client.PutObject(r.Context(), &s3.PutObjectInput{
		Bucket:      &cfg.s3Bucket,
		Key:         aws.String(filename),
		Body:        createdFile,
		ContentType: aws.String("video/mp4"),
	})

	s3VideoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", cfg.s3Bucket, cfg.s3Region, filename)
	metadata.VideoURL = &s3VideoURL
	err = cfg.db.UpdateVideo(metadata)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "unable to upload video", err)
		return
	}
	respondWithJSON(w, http.StatusOK, metadata)
}

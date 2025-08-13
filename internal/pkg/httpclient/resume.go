package httpclient

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"

	"numind-server/internal/pkg/log"
)

// ResumeDownload 断点续传下载
type ResumeDownload struct {
	client *Client
}

// NewResumeDownload 创建断点续传下载器
func NewResumeDownload(client *Client) *ResumeDownload {
	return &ResumeDownload{
		client: client,
	}
}

// Download 下载文件，支持断点续传
func (rd *ResumeDownload) Download(req *Request, filepath string) error {
	// 检查文件是否存在，获取已下载的大小
	var downloadedSize int64
	if fi, err := os.Stat(filepath); err == nil {
		downloadedSize = fi.Size()
		log.Infow("File exists, resuming download", "filepath", filepath, "size", downloadedSize)
	}

	// 设置Range header
	if downloadedSize > 0 {
		if req.Headers == nil {
			req.Headers = make(map[string]string)
		}
		req.Headers["Range"] = fmt.Sprintf("bytes=%d-", downloadedSize)
	}

	// 执行请求
	resp, err := rd.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	// 检查是否支持断点续传
	if resp.StatusCode == http.StatusPartialContent {
		log.Infow("Server supports resume", "filepath", filepath, "resume_from", downloadedSize)
	} else if downloadedSize > 0 {
		// 如果不支持断点续传，重新开始下载
		log.Infow("Server doesn't support resume, starting from beginning", "filepath", filepath)
		downloadedSize = 0
		// 删除已存在的文件
		if err := os.Remove(filepath); err != nil {
			log.Warnw("Failed to remove existing file", "filepath", filepath, "error", err)
		}
	}

	// 打开文件进行写入
	flag := os.O_CREATE | os.O_WRONLY
	if downloadedSize > 0 {
		flag |= os.O_APPEND
	}

	file, err := os.OpenFile(filepath, flag, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// 获取总文件大小
	totalSize := downloadedSize
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		if size, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
			totalSize += size
		}
	}

	// 下载文件
	buffer := make([]byte, 32*1024) // 32KB buffer
	var downloaded int64
	startTime := time.Now()

	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write to file: %w", writeErr)
			}
			downloaded += int64(n)

			// 记录进度
			if totalSize > 0 {
				progress := float64(downloaded+downloadedSize) / float64(totalSize) * 100
				elapsed := time.Since(startTime)
				speed := float64(downloaded+downloadedSize) / elapsed.Seconds() / 1024 / 1024 // MB/s

				log.Debugw("Download progress",
					"filepath", filepath,
					"progress", fmt.Sprintf("%.2f%%", progress),
					"downloaded", downloaded+downloadedSize,
					"total", totalSize,
					"speed", fmt.Sprintf("%.2f MB/s", speed))
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}
	}

	elapsed := time.Since(startTime)
	totalDownloaded := downloaded + downloadedSize
	speed := float64(totalDownloaded) / elapsed.Seconds() / 1024 / 1024 // MB/s

	log.Infow("Download completed",
		"filepath", filepath,
		"total_size", totalDownloaded,
		"elapsed", elapsed,
		"speed", fmt.Sprintf("%.2f MB/s", speed))

	return nil
}

// DownloadWithProgress 带进度回调的下载
func (rd *ResumeDownload) DownloadWithProgress(req *Request, filepath string, progressCallback func(progress float64, downloaded, total int64)) error {
	var downloadedSize int64
	if fi, err := os.Stat(filepath); err == nil {
		downloadedSize = fi.Size()
	}

	if downloadedSize > 0 {
		if req.Headers == nil {
			req.Headers = make(map[string]string)
		}
		req.Headers["Range"] = fmt.Sprintf("bytes=%d-", downloadedSize)
	}

	resp, err := rd.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusPartialContent {
		log.Infow("Resuming download", "filepath", filepath, "resume_from", downloadedSize)
	} else if downloadedSize > 0 {
		log.Infow("Server doesn't support resume, starting from beginning", "filepath", filepath)
		downloadedSize = 0
		if err := os.Remove(filepath); err != nil {
			log.Warnw("Failed to remove existing file", "filepath", filepath, "error", err)
		}
	}

	flag := os.O_CREATE | os.O_WRONLY
	if downloadedSize > 0 {
		flag |= os.O_APPEND
	}

	file, err := os.OpenFile(filepath, flag, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	totalSize := downloadedSize
	if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
		if size, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
			totalSize += size
		}
	}

	buffer := make([]byte, 32*1024)
	var downloaded int64
	lastProgress := -1.0

	for {
		n, err := resp.Body.Read(buffer)
		if n > 0 {
			if _, writeErr := file.Write(buffer[:n]); writeErr != nil {
				return fmt.Errorf("failed to write to file: %w", writeErr)
			}
			downloaded += int64(n)

			// 调用进度回调
			if totalSize > 0 && progressCallback != nil {
				progress := float64(downloaded+downloadedSize) / float64(totalSize) * 100
				// 只在进度变化超过1%时调用回调
				if progress-lastProgress >= 1.0 {
					progressCallback(progress, downloaded+downloadedSize, totalSize)
					lastProgress = progress
				}
			}
		}

		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("failed to read response body: %w", err)
		}
	}

	// 最终进度回调
	if progressCallback != nil {
		progressCallback(100.0, downloaded+downloadedSize, totalSize)
	}

	return nil
}

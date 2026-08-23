package main

import "strings"

type detectionItem struct {
	SourceFile      string `json:"source_file"`
	DetectionReason string `json:"detection_reason"`
}

// parse_detection_log converts the stable, human-readable detection blocks
// emitted by scan_once.sh into ordered records suitable for history.db.
func parse_detection_log(content string) []detectionItem {
	detections := make([]detectionItem, 0)
	current := detectionItem{}
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "Source file") {
			if current.SourceFile != "" || current.DetectionReason != "" {
				detections = append(detections, current)
			}
			current = detectionItem{SourceFile: detectionLineValue(line)}
			continue
		}
		if strings.Contains(line, "Detection reason") {
			current.DetectionReason = detectionLineValue(line)
		}
	}
	if current.SourceFile != "" || current.DetectionReason != "" {
		detections = append(detections, current)
	}
	return detections
}

func detectionLineValue(line string) string {
	pos := strings.LastIndex(line, ":")
	if pos < 0 {
		return ""
	}
	return strings.TrimSpace(line[pos+1:])
}

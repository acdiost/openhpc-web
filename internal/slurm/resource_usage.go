package slurm

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/openhpc-web/openhpc-web/internal/cluster"
)

const (
	sstatFieldCount    = 7
	maxSstatSteps      = 1024
	maxSstatLineLength = 8 * maxDetailFieldLength
)

func (c *Client) JobResourceUsage(parent context.Context, id int64) (cluster.JobResourceUsage, error) {
	if id <= 0 {
		return cluster.JobResourceUsage{}, errors.New("job ID must be positive")
	}
	ctx, cancel := context.WithTimeout(parent, c.timeout)
	defer cancel()
	jobID := strconv.FormatInt(id, 10)
	output, err := c.run(ctx, "sstat",
		"--jobs="+jobID,
		"--allsteps",
		"--noheader",
		"--parsable2",
		"--format=JobID,AveCPU,AveRSS,MaxRSS,AveVMSize,MaxVMSize,TRESUsageInTot",
	)
	if err != nil {
		return cluster.JobResourceUsage{}, fmt.Errorf("read Slurm job resources: %w", err)
	}
	usage, err := parseSstat(output, id, c.now())
	if err != nil {
		return cluster.JobResourceUsage{}, fmt.Errorf("parse Slurm job resources: %w", err)
	}
	return usage, nil
}

func parseSstat(output []byte, id int64, sampledAt time.Time) (cluster.JobResourceUsage, error) {
	jobID := strconv.FormatInt(id, 10)
	usage := cluster.JobResourceUsage{
		JobID: jobID, SampledAt: sampledAt.UTC().Format(time.RFC3339), Steps: []cluster.JobResourceStep{},
	}
	steps := make([]cluster.JobResourceStep, 0)
	scanner := bufio.NewScanner(bytes.NewReader(output))
	scanner.Buffer(make([]byte, 1024), maxSstatLineLength)
	for scanner.Scan() {
		if len(steps) == maxSstatSteps {
			return cluster.JobResourceUsage{}, errors.New("sstat step count exceeds maximum")
		}
		line := strings.TrimSuffix(scanner.Text(), "\r")
		fields := strings.Split(line, "|")
		if len(fields) != sstatFieldCount {
			return cluster.JobResourceUsage{}, errors.New("sstat row has unexpected field count")
		}
		stepID := strings.TrimSpace(fields[0])
		if stepID != jobID && !strings.HasPrefix(stepID, jobID+".") {
			return cluster.JobResourceUsage{}, errors.New("sstat row does not match requested job")
		}
		stepName := strings.TrimPrefix(stepID, jobID+".")
		if stepName == jobID {
			stepName = "job"
		}
		if stepName == "" || len(stepName) > maxDetailFieldLength {
			return cluster.JobResourceUsage{}, errors.New("sstat step name is invalid")
		}
		aveCPUSeconds, err := parseSstatDuration(fields[1])
		if err != nil {
			return cluster.JobResourceUsage{}, err
		}
		totalCPU, err := parseSstatTRESCPU(fields[6])
		if err != nil {
			return cluster.JobResourceUsage{}, err
		}
		totalCPUSeconds, err := parseSstatDuration(totalCPU)
		if err != nil {
			return cluster.JobResourceUsage{}, err
		}
		sizes := make([]int64, 4)
		for index := range sizes {
			sizes[index], err = parseSstatSize(fields[index+2])
			if err != nil {
				return cluster.JobResourceUsage{}, err
			}
		}
		step := cluster.JobResourceStep{
			Step: stepName, AveCPU: strings.TrimSpace(fields[1]), AveCPUSeconds: aveCPUSeconds,
			TotalCPU: totalCPU, TotalCPUSeconds: totalCPUSeconds,
			AveRSS: strings.TrimSpace(fields[2]), AveRSSBytes: sizes[0], MaxRSS: strings.TrimSpace(fields[3]), MaxRSSBytes: sizes[1],
			AveVMSize: strings.TrimSpace(fields[4]), AveVMSizeBytes: sizes[2], MaxVMSize: strings.TrimSpace(fields[5]), MaxVMSizeBytes: sizes[3],
		}
		if totalCPUSeconds > math.MaxInt64-usage.TotalCPUSeconds {
			return cluster.JobResourceUsage{}, errors.New("sstat CPU time exceeds supported range")
		}
		usage.TotalCPUSeconds += totalCPUSeconds
		if step.MaxRSSBytes > usage.MaxRSSBytes {
			usage.MaxRSSBytes = step.MaxRSSBytes
		}
		steps = append(steps, step)
	}
	if err := scanner.Err(); err != nil {
		return cluster.JobResourceUsage{}, fmt.Errorf("scan sstat output: %w", err)
	}
	usage.Steps = steps
	return usage, nil
}

func parseSstatTRESCPU(value string) (string, error) {
	value = strings.TrimSpace(value)
	if isUnavailableSstatValue(value) {
		return value, nil
	}
	if len(value) > maxDetailFieldLength {
		return "", errors.New("sstat TRES usage exceeds maximum length")
	}
	for _, item := range strings.Split(value, ",") {
		name, amount, found := strings.Cut(strings.TrimSpace(item), "=")
		if found && name == "cpu" {
			return strings.TrimSpace(amount), nil
		}
	}
	return "", errors.New("sstat TRES usage does not include CPU time")
}

func parseSstatDuration(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if isUnavailableSstatValue(value) {
		return 0, nil
	}
	dayParts := strings.Split(value, "-")
	if len(dayParts) > 2 {
		return 0, errors.New("sstat CPU time is invalid")
	}
	days := int64(0)
	timePart := dayParts[0]
	var err error
	if len(dayParts) == 2 {
		days, err = strconv.ParseInt(dayParts[0], 10, 64)
		if err != nil || days < 0 {
			return 0, errors.New("sstat CPU time is invalid")
		}
		timePart = dayParts[1]
	}
	parts := strings.Split(timePart, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, errors.New("sstat CPU time is invalid")
	}
	values := make([]int64, len(parts))
	for index, part := range parts {
		values[index], err = strconv.ParseInt(part, 10, 64)
		if err != nil || values[index] < 0 {
			return 0, errors.New("sstat CPU time is invalid")
		}
	}
	hours, minutes, seconds := int64(0), values[0], values[1]
	if len(values) == 3 {
		hours, minutes, seconds = values[0], values[1], values[2]
	}
	if minutes >= 60 || seconds >= 60 || days > math.MaxInt64/86400 || hours > math.MaxInt64/3600 {
		return 0, errors.New("sstat CPU time is invalid")
	}
	total := days * 86400
	if hours*3600 > math.MaxInt64-total {
		return 0, errors.New("sstat CPU time exceeds supported range")
	}
	total += hours * 3600
	if minutes > (math.MaxInt64-total)/60 || seconds > math.MaxInt64-total-minutes*60 {
		return 0, errors.New("sstat CPU time exceeds supported range")
	}
	return total + minutes*60 + seconds, nil
}

func parseSstatSize(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "0" || isUnavailableSstatValue(value) {
		return 0, nil
	}
	unit := value[len(value)-1]
	power := strings.IndexByte("KMGTPE", unit) + 1
	if power == 0 {
		return 0, errors.New("sstat memory size has an unsupported unit")
	}
	number, err := strconv.ParseFloat(value[:len(value)-1], 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
		return 0, errors.New("sstat memory size is invalid")
	}
	bytes := number * math.Pow(1024, float64(power))
	if bytes > math.MaxInt64 {
		return 0, errors.New("sstat memory size exceeds supported range")
	}
	return int64(math.Round(bytes)), nil
}

func isUnavailableSstatValue(value string) bool {
	return value == "" || strings.EqualFold(value, "N/A") || strings.EqualFold(value, "Unknown")
}

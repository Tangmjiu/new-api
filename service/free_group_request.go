package service

import (
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Opaque task schemas have no protocol-independent output token meter. Reject
// token-bound tasks explicitly rather than counting JSON metadata as usage.
func FreeGroupTaskTokenMeta(c *gin.Context, modelName string) (*types.TokenCountMeta, error) {
	config := GetFreeGroupConfig().Config
	if freeRequiresTokenMeter(config, modelName) {
		return nil, &FreeGroupRejection{Rule: "request.task_token_meter", Status: 400, Recovery: "use_metered_text_protocol_or_disable_token_rules"}
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return nil, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return nil, err
	}
	// Prompt text is used only by the explicitly enabled abuse rules here.
	// Token accounting is disabled on this path; opaque fields are not prompts.
	prompt := gjson.GetBytes(body, "prompt").String()
	return &types.TokenCountMeta{TokenType: types.TokenTypeTokenizer, CombineText: prompt}, nil
}

func freeRequiresTokenMeter(config model.FreeGroupConfig, modelName string) bool {
	return config.Request.MaxTokens.Enabled || config.Request.PromptTokens.Enabled || config.Request.OutputInputRatio.Enabled || config.Rate.TPM.Enabled || config.Rate.HourlyTokens.Enabled || config.Rate.DailyTokens.Enabled || config.Rate.MonthlyTokens.Enabled || (config.Models.PerModelQuota && config.Models.PerModelLimits[modelName].DailyTokens > 0)
}

var freeOutputLimitFields = []string{
	"max_tokens", "max_completion_tokens", "max_output_tokens",
	"generationConfig.maxOutputTokens", "generationConfig.max_output_tokens",
	"generation_config.maxOutputTokens", "generation_config.max_output_tokens",
	"parameters.max_tokens", "extra_body.max_tokens", "extra_body.max_completion_tokens",
	"extra_body.max_output_tokens", "extra_body.generationConfig.maxOutputTokens",
}

func freeCanSetOutputBudget(request dto.Request) bool {
	switch request.(type) {
	case *dto.GeneralOpenAIRequest, *dto.OpenAIResponsesRequest, *dto.ClaudeRequest, *dto.GeminiChatRequest:
		return true
	default:
		return false
	}
}

// Completion limits apply per candidate and per prompt on batch completions.
func freeOutputMultiplicity(c *gin.Context, request dto.Request) (int64, error) {
	if !freeCanSetOutputBudget(request) {
		return 1, nil
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return 0, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return 0, err
	}
	count := int64(1)
	for _, field := range []string{"n", "best_of", "generationConfig.candidateCount", "generationConfig.candidate_count", "generation_config.candidateCount", "generation_config.candidate_count", "extra_body.n", "extra_body.best_of", "extra_body.generationConfig.candidateCount"} {
		value := gjson.GetBytes(body, field)
		if !value.Exists() {
			continue
		}
		n := value.Float()
		if value.Type != gjson.Number || n < 1 || n > constant.MaxRelayOutputTokens || n != math.Trunc(n) {
			return 0, &FreeGroupRejection{Rule: "request.output_count", Current: n, Limit: constant.MaxRelayOutputTokens, Status: 400, Recovery: "valid_request"}
		}
		count = max(count, value.Int())
	}
	if strings.HasSuffix(c.Request.URL.Path, "/completions") && !strings.HasSuffix(c.Request.URL.Path, "/chat/completions") {
		prompts := gjson.GetBytes(body, "prompt")
		if prompts.IsArray() {
			items := prompts.Array()
			if len(items) > 0 && (items[0].Type == gjson.String || items[0].IsArray()) {
				if int64(len(items)) > constant.MaxRelayOutputTokens/count {
					return 0, &FreeGroupRejection{Rule: "request.output_count", Limit: constant.MaxRelayOutputTokens, Status: 400, Recovery: "smaller_request"}
				}
				count *= int64(len(items))
			}
		}
	}
	return count, nil
}

// A quota-derived budget only applies when the caller omitted an output limit.
// It reserves the remaining allowance atomically, without enabling max_tokens.
func freeSetOutputBudget(c *gin.Context, request dto.Request, output int64) error {
	if output < 1 {
		return &FreeGroupRejection{Rule: "request.output_input_ratio", Status: 400, Recovery: "larger_prompt"}
	}
	field := "max_tokens"
	switch request.(type) {
	case *dto.GeneralOpenAIRequest:
		if strings.Contains(c.Request.URL.Path, "/chat/completions") {
			field = "max_completion_tokens"
		}
	case *dto.OpenAIResponsesRequest:
		field = "max_output_tokens"
	case *dto.GeminiChatRequest:
		field = "generationConfig.maxOutputTokens"
	}
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	body, err := storage.Bytes()
	if err != nil {
		return err
	}
	for _, existing := range freeOutputLimitFields {
		if gjson.GetBytes(body, existing).Exists() {
			body, err = sjson.SetBytes(body, existing, output)
			if err != nil {
				return err
			}
		}
	}
	body, err = sjson.SetBytes(body, field, output)
	if err != nil {
		return err
	}
	if err := common.Unmarshal(body, request); err != nil {
		return err
	}
	return common.ReplaceRequestBody(c, body)
}

func FreeGroupRequestValidation(c *gin.Context) error {
	if !IsFreeGroupRequest(c) {
		return nil
	}
	config := GetFreeGroupConfig().Config
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return err
	}
	if config.Request.BodySize.Enabled && storage.Size() > config.Request.BodySize.Limit*1024 {
		return &FreeGroupRejection{Rule: "request.body_size_kb", Current: float64(storage.Size()) / 1024, Limit: float64(config.Request.BodySize.Limit), Status: 413, Recovery: "smaller_request"}
	}
	if config.Request.Multimodal.Enabled && !config.Request.Multimodal.Allowed {
		path := c.Request.URL.Path
		if strings.Contains(path, "/images/") || strings.Contains(path, "/audio/") || strings.Contains(path, "/videos") || strings.HasPrefix(path, "/mj/") {
			return &FreeGroupRejection{Rule: "request.multimodal", Current: 1, Status: 400, Recovery: "text_request"}
		}
		mediaType, parameters, _ := mime.ParseMediaType(c.GetHeader("Content-Type"))
		if mediaType == "multipart/form-data" {
			reader, err := storage.NewReader()
			if err != nil {
				return err
			}
			defer reader.Close()
			parts := multipart.NewReader(reader, parameters["boundary"])
			for {
				part, err := parts.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					return &FreeGroupRejection{Rule: "request.multipart", Status: 400, Recovery: "valid_request"}
				}
				hasFile := part.FileName() != ""
				part.Close()
				if hasFile {
					return &FreeGroupRejection{Rule: "request.multimodal", Current: 1, Status: 400, Recovery: "text_request"}
				}
			}
		}
	}
	body, err := storage.Bytes()
	if err != nil {
		return err
	}
	if !gjson.ValidBytes(body) {
		if !strings.Contains(c.GetHeader("Content-Type"), "json") {
			return nil
		}
		return &FreeGroupRejection{Rule: "request.json", Status: 400, Recovery: "valid_request"}
	}
	if config.Request.Streaming.Enabled && !config.Request.Streaming.Allowed && (gjson.GetBytes(body, "stream").Bool() || strings.Contains(c.Request.URL.Path, ":streamGenerateContent")) {
		return &FreeGroupRejection{Rule: "request.streaming", Current: 1, Limit: 0, Status: 400, Recovery: "disable_stream"}
	}
	changed := false
	found := false
	for _, field := range freeOutputLimitFields {
		value := gjson.GetBytes(body, field)
		if !value.Exists() {
			continue
		}
		found = true
		// Validation of the protocol-wide billing multiplier bounds still runs in
		// the existing request validator. Free adds its independently configured cap.
		n := value.Float()
		if value.Type != gjson.Number || n < 0 || n != math.Trunc(n) || n > 2147483647 {
			return &FreeGroupRejection{Rule: "request." + field, Current: n, Limit: 2147483647, Status: 400, Recovery: "valid_request"}
		}
		if config.Request.MaxTokens.Enabled && n > float64(config.Request.MaxTokens.Limit) {
			if config.Request.MaxTokens.Action != "truncate" {
				return &FreeGroupRejection{Rule: "request.max_tokens", Current: n, Limit: float64(config.Request.MaxTokens.Limit), Status: 400, Recovery: "smaller_request"}
			}
			body, err = sjson.SetBytes(body, field, config.Request.MaxTokens.Limit)
			if err != nil {
				return err
			}
			changed = true
		}
	}
	if config.Request.MaxTokens.Enabled && !found {
		field := "max_tokens"
		path := c.Request.URL.Path
		switch {
		case strings.Contains(path, "/responses"):
			field = "max_output_tokens"
		case strings.Contains(path, "/models/"):
			field = "generationConfig.maxOutputTokens"
		case strings.Contains(path, "/chat/completions"):
			field = "max_completion_tokens"
		}
		// Only generation protocols have an output-token parameter.
		if strings.Contains(path, "/completions") || strings.Contains(path, "/responses") || strings.Contains(path, "/messages") || strings.Contains(path, "generateContent") || strings.Contains(path, "streamGenerateContent") {
			body, err = sjson.SetBytes(body, field, config.Request.MaxTokens.Limit)
			if err != nil {
				return err
			}
			changed = true
		}
	}
	if config.Request.Parameters.Enabled {
		for _, field := range []string{"temperature", "generationConfig.temperature", "generation_config.temperature"} {
			value := gjson.GetBytes(body, field)
			if !value.Exists() {
				continue
			}
			n := value.Float()
			if value.Type != gjson.Number || n < config.Request.Parameters.TemperatureMin || n > config.Request.Parameters.TemperatureMax {
				return &FreeGroupRejection{Rule: "request.temperature", Current: n, Limit: config.Request.Parameters.TemperatureMax, Status: 400, Recovery: "valid_request"}
			}
		}
		for field, limits := range config.Request.Parameters.Ranges {
			value := gjson.GetBytes(body, field)
			if !value.Exists() {
				continue
			}
			if value.Type != gjson.Number || value.Float() < limits.Min || value.Float() > limits.Max {
				return &FreeGroupRejection{Rule: "request.parameter." + field, Current: value.Float(), Limit: limits.Max, Status: 400, Recovery: "valid_request"}
			}
		}
	}
	if config.Request.Multimodal.Enabled && !config.Request.Multimodal.Allowed && freeHasMedia(gjson.ParseBytes(body)) {
		return &FreeGroupRejection{Rule: "request.multimodal", Current: 1, Limit: 0, Status: 400, Recovery: "text_request"}
	}
	if changed {
		return common.ReplaceRequestBody(c, body)
	}
	return nil
}

func freeHasMedia(value gjson.Result) bool {
	if !value.IsObject() && !value.IsArray() {
		return false
	}
	found := false
	value.ForEach(func(key, child gjson.Result) bool {
		switch key.String() {
		case "image_url", "input_audio", "inlineData", "inline_data", "fileData", "file_data", "file_id", "file_data_base64", "attachments":
			if child.Exists() && child.Type != gjson.Null {
				found = true
				return false
			}
		case "modalities", "responseModalities", "response_modalities":
			for _, modality := range child.Array() {
				if modality.Type == gjson.String && strings.ToLower(modality.String()) != "text" {
					found = true
					return false
				}
			}
		case "type":
			switch child.String() {
			case "image", "image_url", "input_image", "input_audio", "audio", "file", "input_file", "video":
				found = true
				return false
			}
		}
		if freeHasMedia(child) {
			found = true
			return false
		}
		return true
	})
	return found
}

func FreeGroupOutputLimit(c *gin.Context) (int64, error) {
	storage, err := common.GetBodyStorage(c)
	if err != nil {
		return 0, err
	}
	body, err := storage.Bytes()
	if err != nil {
		return 0, err
	}
	maximum := int64(0)
	for _, field := range freeOutputLimitFields {
		value := gjson.GetBytes(body, field)
		if value.Exists() {
			n := value.Float()
			if value.Type != gjson.Number || n < 0 || n > 2147483647 || n != math.Trunc(n) {
				return 0, fmt.Errorf("invalid output token limit")
			}
			maximum = max(maximum, value.Int())
		}
	}
	return maximum, nil
}

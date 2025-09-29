package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// TranscriptionProvider represents a plugin interface for transcription services
type TranscriptionProvider interface {
	Name() string
	IsAvailable() bool
	Transcribe(audioPath string) (string, error)
	Configure(config map[string]string) error
}

// TranscriptionConfig holds transcription settings
type TranscriptionConfig struct {
	Enabled         bool                         `json:"enabled"`
	DefaultProvider string                       `json:"default_provider"`
	AutoTranscribe  bool                         `json:"auto_transcribe"`
	ProviderConfigs map[string]map[string]string `json:"provider_configs"`
}

// TranscriptionResult holds the transcription output
type TranscriptionResult struct {
	MemoID        string  `json:"memo_id"`
	Text          string  `json:"text"`
	Provider      string  `json:"provider"`
	Confidence    float64 `json:"confidence,omitempty"`
	Language      string  `json:"language,omitempty"`
	TranscribedAt string  `json:"transcribed_at"`
}

// ============================================================================
// WHISPER.CPP PROVIDER (External Command)
// ============================================================================

type WhisperCppProvider struct {
	execPath  string
	modelPath string
	language  string
}

func NewWhisperCppProvider() *WhisperCppProvider {
	return &WhisperCppProvider{
		language: "en",
	}
}

func (w *WhisperCppProvider) Name() string {
	return "whisper.cpp"
}

func (w *WhisperCppProvider) IsAvailable() bool {
	// Check if whisper executable exists in PATH or configured location
	if w.execPath != "" {
		if _, err := os.Stat(w.execPath); err == nil {
			return true
		}
	}

	// Check common locations
	commonPaths := []string{
		"whisper",
		"./whisper",
		"/usr/local/bin/whisper",
		"/usr/bin/whisper",
		"whisper.exe", // Windows
		"./whisper.exe",
	}

	for _, path := range commonPaths {
		if _, err := exec.LookPath(path); err == nil {
			w.execPath = path
			return true
		}
	}

	return false
}

func (w *WhisperCppProvider) Configure(config map[string]string) error {
	if path, ok := config["exec_path"]; ok {
		w.execPath = path
	}
	if path, ok := config["model_path"]; ok {
		w.modelPath = path
	}
	if lang, ok := config["language"]; ok {
		w.language = lang
	}
	return nil
}

func (w *WhisperCppProvider) Transcribe(audioPath string) (string, error) {
	if !w.IsAvailable() {
		return "", fmt.Errorf("whisper.cpp not found in PATH")
	}

	args := []string{"-f", audioPath}

	// Add model path if configured
	if w.modelPath != "" {
		args = append(args, "-m", w.modelPath)
	}

	// Add language
	args = append(args, "-l", w.language)

	// Output to text file
	args = append(args, "-otxt")

	cmd := exec.Command(w.execPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("whisper.cpp failed: %v\nOutput: %s", err, output)
	}

	// Read the generated text file
	txtFile := strings.TrimSuffix(audioPath, filepath.Ext(audioPath)) + ".txt"
	text, err := os.ReadFile(txtFile)
	if err != nil {
		return "", fmt.Errorf("failed to read transcription: %v", err)
	}

	// Clean up temp file
	os.Remove(txtFile)

	return strings.TrimSpace(string(text)), nil
}

// ============================================================================
// VOSK PROVIDER (External Command)
// ============================================================================

type VoskProvider struct {
	execPath  string
	modelPath string
}

func NewVoskProvider() *VoskProvider {
	return &VoskProvider{}
}

func (v *VoskProvider) Name() string {
	return "vosk"
}

func (v *VoskProvider) IsAvailable() bool {
	if v.execPath != "" {
		if _, err := os.Stat(v.execPath); err == nil {
			return true
		}
	}

	// Check for vosk-transcriber or similar
	commonPaths := []string{
		"vosk-transcriber",
		"vosk",
		"./vosk-transcriber",
		"vosk-transcriber.exe", // Windows
	}

	for _, path := range commonPaths {
		if _, err := exec.LookPath(path); err == nil {
			v.execPath = path
			return true
		}
	}

	return false
}

func (v *VoskProvider) Configure(config map[string]string) error {
	if path, ok := config["exec_path"]; ok {
		v.execPath = path
	}
	if path, ok := config["model_path"]; ok {
		v.modelPath = path
	}
	return nil
}

func (v *VoskProvider) Transcribe(audioPath string) (string, error) {
	if !v.IsAvailable() {
		return "", fmt.Errorf("vosk not found")
	}

	args := []string{audioPath}
	if v.modelPath != "" {
		args = append([]string{"-m", v.modelPath}, args...)
	}

	cmd := exec.Command(v.execPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("vosk failed: %v", err)
	}

	return strings.TrimSpace(string(output)), nil
}

// ============================================================================
// PYTHON SCRIPT PROVIDER (for users with custom scripts)
// ============================================================================

type PythonScriptProvider struct {
	scriptPath string
}

func NewPythonScriptProvider() *PythonScriptProvider {
	return &PythonScriptProvider{}
}

func (p *PythonScriptProvider) Name() string {
	return "python_script"
}

func (p *PythonScriptProvider) IsAvailable() bool {
	if p.scriptPath == "" {
		return false
	}

	if _, err := os.Stat(p.scriptPath); err != nil {
		return false
	}

	// Check if python is available
	for _, pythonCmd := range []string{"python3", "python", "py"} {
		if _, err := exec.LookPath(pythonCmd); err == nil {
			return true
		}
	}

	return false
}

func (p *PythonScriptProvider) Configure(config map[string]string) error {
	if path, ok := config["script_path"]; ok {
		p.scriptPath = path
	}
	return nil
}

func (p *PythonScriptProvider) Transcribe(audioPath string) (string, error) {
	if !p.IsAvailable() {
		return "", fmt.Errorf("python script not configured or python not found")
	}

	// Try python commands in order of preference
	var pythonCmd string
	for _, cmd := range []string{"python3", "python", "py"} {
		if _, err := exec.LookPath(cmd); err == nil {
			pythonCmd = cmd
			break
		}
	}

	if pythonCmd == "" {
		return "", fmt.Errorf("no python interpreter found")
	}

	cmd := exec.Command(pythonCmd, p.scriptPath, audioPath)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("python script failed: %v\nOutput: %s", err, output)
	}

	return strings.TrimSpace(string(output)), nil
}

// ============================================================================
// OPENAI WHISPER API PROVIDER (via Python)
// ============================================================================

type OpenAIWhisperProvider struct {
	apiKey     string
	pythonPath string
}

func NewOpenAIWhisperProvider() *OpenAIWhisperProvider {
	return &OpenAIWhisperProvider{}
}

func (o *OpenAIWhisperProvider) Name() string {
	return "openai_whisper"
}

func (o *OpenAIWhisperProvider) IsAvailable() bool {
	// Check if API key is configured
	if o.apiKey == "" {
		if envKey := os.Getenv("OPENAI_API_KEY"); envKey == "" {
			return false
		}
	}

	// Check if python is available
	for _, pythonCmd := range []string{"python3", "python", "py"} {
		if _, err := exec.LookPath(pythonCmd); err == nil {
			return true
		}
	}

	return false
}

func (o *OpenAIWhisperProvider) Configure(config map[string]string) error {
	if key, ok := config["api_key"]; ok {
		o.apiKey = key
	}
	if path, ok := config["python_path"]; ok {
		o.pythonPath = path
	}
	return nil
}

func (o *OpenAIWhisperProvider) Transcribe(audioPath string) (string, error) {
	if !o.IsAvailable() {
		return "", fmt.Errorf("OpenAI Whisper API not configured")
	}

	// Create a temporary Python script for OpenAI API call
	script := `
import openai
import sys
import os

# Set API key
api_key = os.getenv('OPENAI_API_KEY')
if not api_key:
	api_key = '` + o.apiKey + `'

client = openai.OpenAI(api_key=api_key)

# Transcribe audio file
try:
	with open(sys.argv[1], 'rb') as audio_file:
		transcript = client.audio.transcriptions.create(
			model="whisper-1",
			file=audio_file
		)
	print(transcript.text)
except Exception as e:
	print(f"Error: {e}", file=sys.stderr)
	sys.exit(1)
`

	// Write temp script
	tempScript := filepath.Join(os.TempDir(), "voicelog_openai_transcribe.py")
	if err := os.WriteFile(tempScript, []byte(script), 0600); err != nil {
		return "", fmt.Errorf("failed to create temp script: %v", err)
	}
	defer os.Remove(tempScript)

	// Find python command
	var pythonCmd string
	for _, cmd := range []string{"python3", "python", "py"} {
		if _, err := exec.LookPath(cmd); err == nil {
			pythonCmd = cmd
			break
		}
	}

	// Set API key in environment if configured
	env := os.Environ()
	if o.apiKey != "" {
		env = append(env, "OPENAI_API_KEY="+o.apiKey)
	}

	cmd := exec.Command(pythonCmd, tempScript, audioPath)
	cmd.Env = env
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("OpenAI Whisper API failed: %v\nOutput: %s", err, output)
	}

	return strings.TrimSpace(string(output)), nil
}

// ============================================================================
// TRANSCRIPTION MANAGER
// ============================================================================

type TranscriptionManager struct {
	providers map[string]TranscriptionProvider
	config    TranscriptionConfig
	configDir string
}

func NewTranscriptionManager(configDir string) *TranscriptionManager {
	tm := &TranscriptionManager{
		providers: make(map[string]TranscriptionProvider),
		configDir: configDir,
		config: TranscriptionConfig{
			Enabled:         false,
			DefaultProvider: "",
			AutoTranscribe:  false,
			ProviderConfigs: make(map[string]map[string]string),
		},
	}

	// Register available providers
	tm.RegisterProvider(NewWhisperCppProvider())
	tm.RegisterProvider(NewVoskProvider())
	tm.RegisterProvider(NewPythonScriptProvider())
	tm.RegisterProvider(NewOpenAIWhisperProvider())

	// Load config
	tm.LoadConfig()

	// Configure providers from saved config
	for name, provider := range tm.providers {
		if providerConfig, ok := tm.config.ProviderConfigs[name]; ok {
			provider.Configure(providerConfig)
		}
	}

	return tm
}

func (tm *TranscriptionManager) RegisterProvider(provider TranscriptionProvider) {
	tm.providers[provider.Name()] = provider
}

func (tm *TranscriptionManager) GetAvailableProviders() []string {
	var available []string
	for name, provider := range tm.providers {
		if provider.IsAvailable() {
			available = append(available, name)
		}
	}
	return available
}

func (tm *TranscriptionManager) GetAllProviders() []string {
	var all []string
	for name := range tm.providers {
		all = append(all, name)
	}
	return all
}

func (tm *TranscriptionManager) IsProviderAvailable(name string) bool {
	if provider, ok := tm.providers[name]; ok {
		return provider.IsAvailable()
	}
	return false
}

func (tm *TranscriptionManager) Transcribe(audioPath string, providerName string) (*TranscriptionResult, error) {
	if !tm.config.Enabled {
		return nil, fmt.Errorf("transcription is disabled")
	}

	// Use default provider if none specified
	if providerName == "" {
		providerName = tm.config.DefaultProvider
	}

	if providerName == "" {
		return nil, fmt.Errorf("no default provider configured")
	}

	provider, ok := tm.providers[providerName]
	if !ok {
		return nil, fmt.Errorf("provider not found: %s", providerName)
	}

	if !provider.IsAvailable() {
		return nil, fmt.Errorf("provider not available: %s", providerName)
	}

	text, err := provider.Transcribe(audioPath)
	if err != nil {
		return nil, err
	}

	result := &TranscriptionResult{
		Text:          text,
		Provider:      providerName,
		TranscribedAt: time.Now().Format(time.RFC3339),
	}

	return result, nil
}

func (tm *TranscriptionManager) LoadConfig() error {
	configPath := filepath.Join(tm.configDir, "transcription.json")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Use defaults
		}
		return err
	}

	return json.Unmarshal(data, &tm.config)
}

func (tm *TranscriptionManager) SaveConfig() error {
	configPath := filepath.Join(tm.configDir, "transcription.json")

	data, err := json.MarshalIndent(tm.config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

// ConfigureProvider updates a provider's configuration
func (tm *TranscriptionManager) ConfigureProvider(providerName string, config map[string]string) error {
	provider, ok := tm.providers[providerName]
	if !ok {
		return fmt.Errorf("provider not found: %s", providerName)
	}

	if err := provider.Configure(config); err != nil {
		return err
	}

	// Save to config
	if tm.config.ProviderConfigs == nil {
		tm.config.ProviderConfigs = make(map[string]map[string]string)
	}
	tm.config.ProviderConfigs[providerName] = config
	return tm.SaveConfig()
}

// SetEnabled enables or disables transcription
func (tm *TranscriptionManager) SetEnabled(enabled bool) error {
	tm.config.Enabled = enabled
	return tm.SaveConfig()
}

// SetDefaultProvider sets the default transcription provider
func (tm *TranscriptionManager) SetDefaultProvider(providerName string) error {
	if _, ok := tm.providers[providerName]; !ok {
		return fmt.Errorf("provider not found: %s", providerName)
	}
	tm.config.DefaultProvider = providerName
	return tm.SaveConfig()
}

// SetAutoTranscribe enables or disables auto-transcription
func (tm *TranscriptionManager) SetAutoTranscribe(auto bool) error {
	tm.config.AutoTranscribe = auto
	return tm.SaveConfig()
}

// GetConfig returns the current transcription configuration
func (tm *TranscriptionManager) GetConfig() TranscriptionConfig {
	return tm.config
}

// ShowSetupInstructions prints setup instructions for transcription providers
func ShowTranscriptionSetupInstructions() {
	fmt.Println("=== VoiceLog Transcription Setup ===")
	fmt.Println()
	fmt.Println("VoiceLog supports optional transcription through external tools.")
	fmt.Println("No installation required - configure only if you want transcription.")
	fmt.Println()
	fmt.Println("Supported transcription engines:")
	fmt.Println()
	fmt.Println("1. whisper.cpp (Recommended - Local, Private)")
	fmt.Println("   - High accuracy, supports many languages")
	fmt.Println("   - Installation: https://github.com/ggerganov/whisper.cpp")
	fmt.Println("   - Download model: https://huggingface.co/ggerganov/whisper.cpp")
	fmt.Println("   - Quick start:")
	fmt.Println("     git clone https://github.com/ggerganov/whisper.cpp")
	fmt.Println("     cd whisper.cpp && make")
	fmt.Println("     ./models/download-ggml-model.sh base.en")
	fmt.Println()
	fmt.Println("2. Vosk (Lightweight, Offline)")
	fmt.Println("   - Fast, good for real-time transcription")
	fmt.Println("   - Installation: https://alphacephei.com/vosk/")
	fmt.Println("   - Download models from: https://alphacephei.com/vosk/models")
	fmt.Println()
	fmt.Println("3. OpenAI Whisper API (Cloud-based)")
	fmt.Println("   - Highest accuracy, requires internet & API key")
	fmt.Println("   - Set OPENAI_API_KEY environment variable")
	fmt.Println("   - Install: pip install openai")
	fmt.Println()
	fmt.Println("4. Custom Python Script")
	fmt.Println("   - Use your own script with any API (AssemblyAI, Rev.ai, etc.)")
	fmt.Println("   - Script should accept audio file path and output text")
	fmt.Println("   - Example template available in documentation")
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println("   Press Ctrl+S -> Navigate to 'Transcription Settings'")
	fmt.Println("   Enable transcription and select your provider")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("   Press Ctrl+T to transcribe the selected memo")
	fmt.Println("   Enable auto-transcribe to automatically transcribe new recordings")
	fmt.Println()
}

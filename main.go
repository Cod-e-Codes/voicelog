package main

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/gordonklaus/portaudio"
)

// Application constants
const (
	AppName      = "voicelog"
	ConfigDir    = ".voicelog"
	MemosDir     = "memos"
	ConfigFile   = "config.json"
	MetadataFile = "metadata.json"
	LogFile      = "voicelog.log"

	// Audio settings
	SampleRate   = 44100
	ChannelCount = 2
	BitDepth     = 2 // 16-bit
)

// Setup logging
func setupLogging() {
	homeDir, _ := os.UserHomeDir()
	logDir := filepath.Join(homeDir, ConfigDir)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return
	}

	logPath := filepath.Join(logDir, LogFile)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		return
	}

	log.SetOutput(logFile)
	log.SetFlags(log.LstdFlags | log.Lshortfile)
}

// Application state
type AppState int

const (
	StateViewing AppState = iota
	StateRecording
	StatePlaying
	StateRenaming
	StateTagging
	StateSettings
)

// Audio formats
type AudioFormat int

const (
	FormatWAV AudioFormat = iota
	FormatMP3
	FormatOGG
)

func (f AudioFormat) String() string {
	switch f {
	case FormatWAV:
		return "WAV"
	case FormatMP3:
		return "MP3"
	case FormatOGG:
		return "OGG"
	default:
		return "WAV"
	}
}

func (f AudioFormat) Extension() string {
	switch f {
	case FormatWAV:
		return ".wav"
	case FormatMP3:
		return ".mp3"
	case FormatOGG:
		return ".ogg"
	default:
		return ".wav"
	}
}

// Memo represents a voice memo with metadata
type Memo struct {
	ID       string    `json:"id"`
	Filename string    `json:"filename"`
	Name     string    `json:"title"` // Changed from Title to Name to avoid conflict
	Duration float64   `json:"duration"`
	Created  time.Time `json:"created"`
	Size     int64     `json:"size"`
	Tags     []string  `json:"tags"`
	Format   string    `json:"format"`
}

// Implement list.Item interface
func (m Memo) Title() string {
	return truncateText(m.Name, 30) // Limit title to 30 characters
}

func (m Memo) Description() string {
	duration := formatDuration(time.Duration(m.Duration * float64(time.Second)))
	size := formatBytes(m.Size)
	tags := ""
	if len(m.Tags) > 0 {
		// Truncate tags if they're too long
		tagString := strings.Join(m.Tags, ", ")
		if len(tagString) > 20 {
			tagString = tagString[:17] + "..."
		}
		tags = " [" + tagString + "]"
	}
	return fmt.Sprintf("%s, %s%s", duration, size, tags)
}

func (m Memo) FilterValue() string {
	return m.Name + " " + strings.Join(m.Tags, " ")
}

// Truncate text to specified length
func truncateText(text string, maxLength int) string {
	if len(text) <= maxLength {
		return text
	}
	return text[:maxLength-3] + "..."
}

// Show notification to user
func (m *Model) showNotification(message string) {
	m.notification = message
	m.notificationAt = time.Now()
}

// Placeholder memo for empty list
type placeholderMemo struct{}

func (p placeholderMemo) Title() string {
	return "No memos found. Press SPACE to record your first memo!"
}

func (p placeholderMemo) Description() string {
	return ""
}

func (p placeholderMemo) FilterValue() string {
	return "no memos found press space to record"
}

// AudioDeviceInfo represents an audio device
type AudioDeviceInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"is_default"`
	IsInput   bool   `json:"is_input"`
	IsOutput  bool   `json:"is_output"`
}

// Config holds application configuration
type Config struct {
	DefaultFormat AudioFormat       `json:"default_format"`
	MemosPath     string            `json:"memos_path"`
	Theme         string            `json:"theme"`
	Keybindings   Keybindings       `json:"keybindings"`
	InputDevice   string            `json:"input_device"`
	OutputDevice  string            `json:"output_device"`
	SampleRate    int               `json:"sample_rate"`
	BitDepth      int               `json:"bit_depth"`
	ChannelCount  int               `json:"channel_count"`
	Volume        float64           `json:"volume"`
	AudioDevices  []AudioDeviceInfo `json:"audio_devices"`

	// Audio processing settings
	AutoNormalize      bool    `json:"auto_normalize"`
	NormalizeTarget    float64 `json:"normalize_target"`  // Target peak level (0.0-1.0)
	SilenceThreshold   float64 `json:"silence_threshold"` // RMS threshold for silence detection
	AutoTrimSilence    bool    `json:"auto_trim_silence"`
	ClippingThreshold  float64 `json:"clipping_threshold"`   // Threshold for clipping detection (0.95-1.0)
	WaveformSampleRate int     `json:"waveform_sample_rate"` // Samples to display in waveform
}

// Keybindings holds custom key configurations
type Keybindings struct {
	Record string `json:"record"`
	Play   string `json:"play"`
	Stop   string `json:"stop"`
	Delete string `json:"delete"`
	Rename string `json:"rename"`
	Tag    string `json:"tag"`
	Export string `json:"export"`
	Help   string `json:"help"`
	Quit   string `json:"quit"`
}

// Detect available audio devices using PortAudio
func detectAudioDevices() []AudioDeviceInfo {
	var devices []AudioDeviceInfo

	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		// Fallback if initialization fails
		return append(devices, AudioDeviceInfo{
			ID:        "default",
			Name:      "Default Device (Fallback)",
			IsDefault: true,
			IsInput:   true,
			IsOutput:  true,
		})
	}
	defer func() {
		if err := portaudio.Terminate(); err != nil {
			log.Printf("Error terminating PortAudio: %v", err)
		}
	}()

	// Get host APIs (e.g., ALSA on Linux, CoreAudio on macOS)
	hostApis, err := portaudio.HostApis()
	if err != nil {
		return append(devices, AudioDeviceInfo{
			ID:        "default",
			Name:      "Default Device (Error)",
			IsDefault: true,
			IsInput:   true,
			IsOutput:  true,
		})
	}

	// Get default devices
	defaultInput, _ := portaudio.DefaultInputDevice()
	defaultOutput, _ := portaudio.DefaultOutputDevice()

	// Enumerate devices from all host APIs
	for _, host := range hostApis {
		log.Printf("Host API: %s", host.Name)
		for _, dev := range host.Devices {
			// Skip devices with no I/O channels
			if dev.MaxInputChannels == 0 && dev.MaxOutputChannels == 0 {
				continue
			}

			// Create device info
			info := AudioDeviceInfo{
				ID:   fmt.Sprintf("%d", dev.Index), // Unique ID based on PortAudio index
				Name: fmt.Sprintf("%s (%s)", dev.Name, host.Name),
				IsDefault: (defaultInput != nil && dev.Index == defaultInput.Index) ||
					(defaultOutput != nil && dev.Index == defaultOutput.Index),
				IsInput:  dev.MaxInputChannels > 0,
				IsOutput: dev.MaxOutputChannels > 0,
			}
			log.Printf("Found device: ID=%s, Name=%s, Input=%v, Output=%v, Channels=%d",
				info.ID, info.Name, info.IsInput, info.IsOutput, dev.MaxInputChannels)
			devices = append(devices, info)
		}
	}

	// If no devices found, add a fallback
	if len(devices) == 0 {
		devices = append(devices, AudioDeviceInfo{
			ID:        "default",
			Name:      "Default Device (No devices found)",
			IsDefault: true,
			IsInput:   true,
			IsOutput:  true,
		})
	}

	return devices
}

// Set default devices in config
func setDefaultDevices(config *Config) {
	log.Printf("Setting default devices. Current InputDevice: %s, OutputDevice: %s",
		config.InputDevice, config.OutputDevice)

	// Find first available input device
	for _, device := range config.AudioDevices {
		if device.IsInput && config.InputDevice == "" {
			config.InputDevice = device.ID
			log.Printf("Set default input device: %s (%s)", device.ID, device.Name)
			break
		}
	}

	// Find first available output device
	for _, device := range config.AudioDevices {
		if device.IsOutput && config.OutputDevice == "" {
			config.OutputDevice = device.ID
			log.Printf("Set default output device: %s (%s)", device.ID, device.Name)
			break
		}
	}

	log.Printf("Final devices - Input: %s, Output: %s",
		config.InputDevice, config.OutputDevice)
}

// Get device by ID from PortAudio
func getDeviceByID(deviceID string) *portaudio.DeviceInfo {
	devices, err := portaudio.Devices()
	if err != nil {
		return nil
	}

	deviceIdx, err := strconv.Atoi(deviceID)
	if err != nil {
		return nil
	}

	if deviceIdx >= 0 && deviceIdx < len(devices) {
		return devices[deviceIdx]
	}

	return nil
}

// Default configuration
func defaultConfig() Config {
	homeDir, _ := os.UserHomeDir()
	return Config{
		DefaultFormat: FormatWAV,
		MemosPath:     filepath.Join(homeDir, ConfigDir, MemosDir),
		Theme:         "dark",
		InputDevice:   "", // Will be set to first available input device when needed
		OutputDevice:  "", // Will be set to first available output device when needed
		SampleRate:    SampleRate,
		BitDepth:      BitDepth,
		ChannelCount:  ChannelCount,
		Volume:        1.0, // Default volume (100%)
		Keybindings: Keybindings{
			Record: " ", // spacebar
			Play:   "enter",
			Stop:   "s",
			Delete: "d",
			Rename: "r",
			Tag:    "t",
			Export: "e",
			Help:   "?",
			Quit:   "q",
		},
		AudioDevices: []AudioDeviceInfo{}, // Empty initially, will be populated when needed

		// Audio processing defaults
		AutoNormalize:      true,
		NormalizeTarget:    0.95, // 95% of max amplitude
		SilenceThreshold:   0.01, // 1% of max amplitude
		AutoTrimSilence:    true,
		ClippingThreshold:  0.95,
		WaveformSampleRate: 100, // 100 points for display
	}
}

// Audio device and context
type AudioDevice struct {
	stream        *portaudio.Stream // PortAudio stream for recording/playback
	recordingFile *os.File          // File for recording audio data
	playbackData  []int16           // Audio data for playback
	playbackPos   int               // Current position in playback data
}

// Waveform data for visualization
type WaveformData struct {
	samples []float32
	max     float32
}

// VU meter data
type VUMeterData struct {
	leftLevel  float32
	rightLevel float32
}

// AudioAnalyzer handles real-time audio analysis for visualization
type AudioAnalyzer struct {
	buffer          []int16 // Circular buffer for incoming samples
	bufferSize      int     // Size of the buffer
	writePos        int     // Current write position
	downsampleRatio int     // Ratio for downsampling
	waveformPoints  int     // Number of points to display
}

// AudioProcessor handles post-recording audio processing
type AudioProcessor struct {
	SilenceThreshold float64
	NormalizeTarget  float64
}

// ClippingDetector monitors audio for clipping in real-time
type ClippingDetector struct {
	threshold    float64
	isClipping   bool
	clipCount    int
	totalSamples int
	flashCounter int
}

// Model represents the application state
type Model struct {
	// Core state
	state       AppState
	config      Config
	memos       []Memo
	selectedIdx int

	// Audio
	audioDevice   *AudioDevice
	recording     bool
	playing       bool
	recordingTime time.Duration
	playbackPos   time.Duration

	// Visualization data
	waveform WaveformData
	vuMeter  VUMeterData

	// Real-time audio analysis
	audioAnalyzer      *AudioAnalyzer
	clippingDetector   *ClippingDetector
	realtimeWaveform   []float32 // Downsampled waveform for display
	playbackWaveform   []float32 // Waveform data for playback visualization
	playbackSampleRate int       // Sample rate of current playback file
	isClipping         bool      // Current clipping status
	peakLevel          float32   // Current peak level

	// UI components
	textInput textinput.Model
	help      help.Model
	memoList  list.Model

	// Settings
	settingsSelectedIdx int
	availableDevices    []AudioDeviceInfo

	// Animation
	recordingPulse int
	lastUpdate     time.Time

	// User notifications
	notification   string
	notificationAt time.Time

	// Dimensions
	width  int
	height int
}

// Key bindings
type keyMap struct {
	Record   key.Binding
	Play     key.Binding
	Stop     key.Binding
	Delete   key.Binding
	Rename   key.Binding
	Tag      key.Binding
	Export   key.Binding
	Help     key.Binding
	Settings key.Binding
	TestFile key.Binding
	Quit     key.Binding
	Up       key.Binding
	Down     key.Binding
	Enter    key.Binding
	Escape   key.Binding
	Left     key.Binding
	Right    key.Binding
}

// ShortHelp returns keybindings to be shown in the mini help view
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Record, k.Play, k.Export, k.Help, k.Quit}
}

// FullHelp returns keybindings for the expanded help view
func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Record, k.Play, k.Stop, k.Up, k.Down}, // Core controls
		{k.Rename, k.Tag, k.Delete, k.Export},    // Management
		{k.Settings, k.TestFile, k.Help, k.Quit}, // Other
	}
}

var keys = keyMap{
	Record: key.NewBinding(
		key.WithKeys(" "),
		key.WithHelp("space", "record/stop"),
	),
	Play: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "play/pause"),
	),
	Stop: key.NewBinding(
		key.WithKeys("ctrl+x"),
		key.WithHelp("ctrl+x", "stop"),
	),
	Delete: key.NewBinding(
		key.WithKeys("ctrl+d"),
		key.WithHelp("ctrl+d", "delete"),
	),
	Rename: key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "rename"),
	),
	Tag: key.NewBinding(
		key.WithKeys("ctrl+g"),
		key.WithHelp("ctrl+g", "tag"),
	),
	Export: key.NewBinding(
		key.WithKeys("ctrl+e"),
		key.WithHelp("ctrl+e", "export"),
	),
	Help: key.NewBinding(
		key.WithKeys("?"),
		key.WithHelp("?", "help"),
	),
	Settings: key.NewBinding(
		key.WithKeys("ctrl+s"),
		key.WithHelp("ctrl+s", "settings"),
	),
	TestFile: key.NewBinding(
		key.WithKeys("ctrl+t"),
		key.WithHelp("ctrl+t", "test file"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	Left: key.NewBinding(
		key.WithKeys("left", "h"),
		key.WithHelp("←/h", "left"),
	),
	Right: key.NewBinding(
		key.WithKeys("right", "l"),
		key.WithHelp("→/l", "right"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
	),
}

// Professional color palette
const (
	// Primary colors
	PrimaryBlue   = "#2563EB" // Professional blue
	PrimaryGreen  = "#059669" // Professional green
	PrimaryPurple = "#7C3AED" // Professional purple

	// Accent colors
	AccentOrange = "#EA580C" // Warm orange
	AccentCyan   = "#0891B2" // Cool cyan
	AccentPink   = "#DB2777" // Vibrant pink

	// Neutral colors
	TextPrimary   = "#F8FAFC" // Light text
	TextSecondary = "#CBD5E1" // Secondary text
	TextMuted     = "#64748B" // Muted text
	Background    = "#0F172A" // Dark background
	Surface       = "#1E293B" // Surface color
	Border        = "#334155" // Border color
)

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(TextPrimary)).
			Background(lipgloss.Color(PrimaryBlue)).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(TextPrimary)).
			Background(lipgloss.Color(AccentPink))

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(TextSecondary))

	mutedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(TextMuted))

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(PrimaryGreen))

	recordingStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(AccentOrange)).
			Bold(true)

	waveformStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(AccentCyan))

	vuMeterStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(PrimaryGreen))

	statusBarStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(TextPrimary)).
			Background(lipgloss.Color(PrimaryBlue)).
			Padding(0, 1)

	borderStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(Border)).
			Padding(1, 2)

	headerBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(PrimaryBlue)).
				Padding(0, 1)

	memoListBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color(Border)).
				Padding(1, 2)
)

// Initialize the application
func initialModel() Model {
	config := loadConfig()

	// Create directories if they don't exist
	if err := os.MkdirAll(config.MemosPath, 0755); err != nil {
		log.Printf("Error creating memos directory: %v", err)
	}

	// Initialize text input
	ti := textinput.New()
	ti.Placeholder = "Enter memo name..."
	ti.Focus()
	ti.CharLimit = 50
	ti.Width = 30

	// Initialize help
	h := help.New()
	h.Width = 80

	// Load existing memos
	memos := loadMemos(config.MemosPath)

	// Initialize memo list
	memoList := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
	memoList.Title = "MEMOS"
	memoList.Styles.Title = titleStyle
	memoList.SetShowHelp(false)         // Disable built-in help since we have status bar
	memoList.SetSize(40, 15)            // Set conservative height to prevent shifting
	memoList.SetFilteringEnabled(false) // Disable filtering
	memoList.SetItems(convertMemosToListItems(memos))

	// Initialize audio processing components
	audioAnalyzer := NewAudioAnalyzer(config.SampleRate, config.WaveformSampleRate)
	clippingDetector := NewClippingDetector(config.ClippingThreshold)

	return Model{
		state:               StateViewing,
		config:              config,
		memos:               memos,
		selectedIdx:         0,
		settingsSelectedIdx: 0,
		availableDevices:    config.AudioDevices, // This will be empty initially
		textInput:           ti,
		help:                h,
		memoList:            memoList,
		lastUpdate:          time.Now(),

		// Audio processing
		audioAnalyzer:    audioAnalyzer,
		clippingDetector: clippingDetector,
		realtimeWaveform: make([]float32, config.WaveformSampleRate),
	}
}

// Convert memos to list items
func convertMemosToListItems(memos []Memo) []list.Item {
	items := make([]list.Item, len(memos))
	for i, memo := range memos {
		items[i] = memo
	}
	return items
}

// Load configuration from file
func loadConfig() Config {
	homeDir, _ := os.UserHomeDir()
	configPath := filepath.Join(homeDir, ConfigDir, ConfigFile)

	// Create default config if doesn't exist
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		config := defaultConfig()
		// Don't detect audio devices during initial config creation
		if err := saveConfig(config); err != nil {
			log.Printf("Error saving default config: %v", err)
		}
		return config
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return defaultConfig()
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return defaultConfig()
	}

	// Ensure audio settings have valid values
	if config.SampleRate <= 0 {
		config.SampleRate = SampleRate
	}
	if config.ChannelCount <= 0 {
		config.ChannelCount = ChannelCount
	}
	if config.BitDepth <= 0 {
		config.BitDepth = BitDepth
	}
	if config.Volume <= 0.0 || config.Volume > 1.0 {
		config.Volume = 1.0
	}

	// Ensure audio processing settings have valid values
	if config.WaveformSampleRate <= 0 {
		config.WaveformSampleRate = 100
	}
	if config.NormalizeTarget <= 0.0 || config.NormalizeTarget > 1.0 {
		config.NormalizeTarget = 0.95
	}
	if config.SilenceThreshold <= 0.0 {
		config.SilenceThreshold = 0.01
	}
	if config.ClippingThreshold <= 0.0 || config.ClippingThreshold > 1.0 {
		config.ClippingThreshold = 0.95
	}

	return config
}

// Read WAV file data
func readWAVData(filePath string) ([]int16, int, int, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()

	// Read WAV header
	header := make([]byte, 44)
	if _, err := file.Read(header); err != nil {
		return nil, 0, 0, err
	}

	// Parse header
	if string(header[0:4]) != "RIFF" || string(header[8:12]) != "WAVE" {
		return nil, 0, 0, fmt.Errorf("not a valid WAV file")
	}

	// Get format info
	channels := int(binary.LittleEndian.Uint16(header[22:24]))
	sampleRate := int(binary.LittleEndian.Uint32(header[24:28]))
	_ = int(binary.LittleEndian.Uint16(header[34:36])) // bitsPerSample - not used but parsed for completeness

	// Read audio data
	dataSize := int64(binary.LittleEndian.Uint32(header[40:44]))
	audioData := make([]byte, dataSize)
	if _, err := file.Read(audioData); err != nil {
		return nil, 0, 0, err
	}

	// Convert to int16 samples
	samples := make([]int16, len(audioData)/2)
	for i := 0; i < len(samples); i++ {
		samples[i] = int16(binary.LittleEndian.Uint16(audioData[i*2 : i*2+2]))
	}

	return samples, sampleRate, channels, nil
}

// Write WAV file header
func writeWAVHeader(file *os.File, sampleRate, channels, bitsPerSample int, dataSize int64) error {
	// WAV header structure
	header := make([]byte, 44)

	// RIFF header
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:8], uint32(36+dataSize))
	copy(header[8:12], "WAVE")

	// fmt chunk
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:20], 16) // fmt chunk size
	binary.LittleEndian.PutUint16(header[20:22], 1)  // audio format (PCM)
	binary.LittleEndian.PutUint16(header[22:24], uint16(channels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(sampleRate*channels*bitsPerSample/8))
	binary.LittleEndian.PutUint16(header[32:34], uint16(channels*bitsPerSample/8))
	binary.LittleEndian.PutUint16(header[34:36], uint16(bitsPerSample))

	// data chunk
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:44], uint32(dataSize))

	_, err := file.Write(header)
	return err
}

// Save configuration to file
func saveConfig(config Config) error {
	homeDir, _ := os.UserHomeDir()
	configDir := filepath.Join(homeDir, ConfigDir)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	configPath := filepath.Join(configDir, ConfigFile)
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

// Load memos from directory
func loadMemos(memosPath string) []Memo {
	var memos []Memo

	// Load metadata
	metadataPath := filepath.Join(memosPath, MetadataFile)
	if data, err := os.ReadFile(metadataPath); err == nil {
		if err := json.Unmarshal(data, &memos); err != nil {
			log.Printf("Error unmarshaling metadata: %v", err)
		}
	}

	// Verify files still exist and update info
	var validMemos []Memo
	for _, memo := range memos {
		filePath := filepath.Join(memosPath, memo.Filename)
		if info, err := os.Stat(filePath); err == nil {
			memo.Size = info.Size()
			validMemos = append(validMemos, memo)
		}
	}

	// Sort by creation date (newest first)
	sort.Slice(validMemos, func(i, j int) bool {
		return validMemos[i].Created.After(validMemos[j].Created)
	})

	return validMemos
}

// Save memos metadata
func saveMemos(memos []Memo, memosPath string) error {
	metadataPath := filepath.Join(memosPath, MetadataFile)
	data, err := json.MarshalIndent(memos, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metadataPath, data, 0644)
}

// Generate filename for new memo
func generateFilename(format AudioFormat) string {
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	return fmt.Sprintf("memo_%s%s", timestamp, format.Extension())
}

// Format bytes to human readable
func formatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(bytes)/float64(div), "KMGTPE"[exp])
}

// Format duration
func formatDuration(d time.Duration) string {
	minutes := int(d.Minutes())
	seconds := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d", minutes, seconds)
}

// Utility functions
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Tea program messages
type tickMsg time.Time
type recordingTickMsg time.Time

// Tick command for animations
func tick() tea.Cmd {
	return tea.Tick(time.Millisecond*100, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// Recording tick command
func recordingTick() tea.Cmd {
	return tea.Tick(time.Millisecond*100, func(t time.Time) tea.Msg {
		return recordingTickMsg(t)
	})
}

// Initialize the program
func (m Model) Init() tea.Cmd {
	return tick()
}

// Update handles messages
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.help.Width = msg.Width
		m.memoList.SetSize(msg.Width, msg.Height-15) // Reserve space for header, status, and help

	case tea.KeyMsg:
		switch m.state {
		case StateRenaming, StateTagging:
			return m.handleTextInput(msg)
		case StateSettings:
			return m.handleSettingsKeys(msg)
		default:
			return m.handleMainKeys(msg)
		}

	case tickMsg:
		now := time.Now()
		if m.recording {
			m.recordingTime = now.Sub(m.lastUpdate) + m.recordingTime
			m.recordingPulse = (m.recordingPulse + 1) % 20
			// Update clipping flash counter
			if m.clippingDetector != nil {
				m.clippingDetector.UpdateFlash()
			}
		}
		if m.playing {
			// Update playback position based on real audio data
			if m.audioDevice != nil && m.audioDevice.playbackData != nil {
				// Use the stored sample rate from when playback started
				sampleRate := m.playbackSampleRate
				if sampleRate == 0 {
					sampleRate = 44100 // Fallback to default sample rate
				}

				// Calculate position based on samples played and actual sample rate
				m.playbackPos = time.Duration(float64(m.audioDevice.playbackPos) / float64(sampleRate) * float64(time.Second))

				// Check if we've reached the end of the audio data
				if m.audioDevice.playbackPos >= len(m.audioDevice.playbackData) {
					log.Printf("Auto-stopping playback - reached end of audio data")
					m.stopPlayback()
				}
			}
		}
		// Clear notifications after 3 seconds
		if !m.notificationAt.IsZero() && now.Sub(m.notificationAt) > 3*time.Second {
			m.notification = ""
			m.notificationAt = time.Time{}
		}

		m.lastUpdate = now
		cmds = append(cmds, tick())

	case recordingTickMsg:
		if m.recording {
			// Update waveform data (simulated)
			m.updateWaveform()
			cmds = append(cmds, recordingTick())
		}
	}

	return m, tea.Batch(cmds...)
}

// Handle settings keyboard input
func (m Model) handleSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Quit):
		m.state = StateViewing
		if err := saveConfig(m.config); err != nil {
			log.Printf("Error saving config: %v", err)
		}

	case key.Matches(msg, keys.Up):
		if m.settingsSelectedIdx > 0 {
			m.settingsSelectedIdx--
		}

	case key.Matches(msg, keys.Down):
		if m.settingsSelectedIdx < 11 { // 12 settings items (0-11)
			m.settingsSelectedIdx++
		}

	case key.Matches(msg, keys.Left):
		// Initialize audio devices before adjusting audio-related settings
		if m.settingsSelectedIdx <= 1 { // Input/Output device settings
			m.initializeAudioDevices()
		}
		m.adjustSetting(-1)

	case key.Matches(msg, keys.Right):
		// Initialize audio devices before adjusting audio-related settings
		if m.settingsSelectedIdx <= 1 { // Input/Output device settings
			m.initializeAudioDevices()
		}
		m.adjustSetting(1)

	case key.Matches(msg, keys.Enter):
		// Initialize audio devices before selecting device-related settings
		if m.settingsSelectedIdx <= 1 { // Input/Output device settings
			m.initializeAudioDevices()
		}
		m.selectSetting()
	}

	return m, nil
}

// Load test file for playback
func (m *Model) loadTestFile() {
	// Create a simple test WAV file with a sine wave
	testFilename := "test_tone.wav"
	testFilePath := filepath.Join(m.config.MemosPath, testFilename)

	// Create test file if it doesn't exist
	if _, err := os.Stat(testFilePath); os.IsNotExist(err) {
		m.createTestToneFile(testFilePath)
	}

	// Create a memo for the test file
	testMemo := Memo{
		ID:       "test_file",
		Filename: testFilename,
		Name:     "Test Tone (440Hz)",
		Duration: 5.0, // 5 seconds
		Created:  time.Now(),
		Size:     441000, // Approximate size
		Tags:     []string{"test"},
		Format:   "WAV",
	}

	// Add to memos list if not already there
	found := false
	for i, memo := range m.memos {
		if memo.ID == "test_file" {
			m.memos[i] = testMemo
			found = true
			break
		}
	}
	if !found {
		m.memos = append([]Memo{testMemo}, m.memos...)
	}

	// Update list items to reflect new/updated test memo
	m.memoList.SetItems(convertMemosToListItems(m.memos))

	m.selectedIdx = 0 // Select the test file

	// Save the updated memos to metadata
	if err := saveMemos(m.memos, m.config.MemosPath); err != nil {
		log.Printf("Error saving memos metadata: %v", err)
	}

	log.Printf("Test file loaded: %s", testFilename)
}

// Create a test tone file (440Hz sine wave)
func (m *Model) createTestToneFile(filePath string) {
	file, err := os.Create(filePath)
	if err != nil {
		log.Printf("Error creating test file: %v", err)
		return
	}
	defer file.Close()

	// WAV parameters
	sampleRate := 44100
	duration := 5.0    // seconds
	frequency := 440.0 // Hz
	amplitude := 0.3

	// Calculate number of samples
	numSamples := int(float64(sampleRate) * duration)

	// Write WAV header
	if err := writeWAVHeader(file, sampleRate, 1, 16, int64(numSamples*2)); err != nil {
		log.Printf("Error writing WAV header: %v", err)
		return
	}

	// Generate sine wave samples
	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		sample := int16(amplitude * 32767 * math.Sin(2*math.Pi*frequency*t))
		if err := binary.Write(file, binary.LittleEndian, sample); err != nil {
			log.Printf("Error writing sample: %v", err)
			return
		}
	}
}

// Adjust setting value
func (m *Model) adjustSetting(delta int) {
	switch m.settingsSelectedIdx {
	case 0: // Input Device
		// Cycle through input devices
		currentIdx := m.findDeviceIndex(m.config.InputDevice)
		if currentIdx >= 0 {
			nextIdx := (currentIdx + delta + len(m.availableDevices)) % len(m.availableDevices)
			m.config.InputDevice = m.availableDevices[nextIdx].ID
		}
	case 1: // Output Device
		// Cycle through output devices
		currentIdx := m.findDeviceIndex(m.config.OutputDevice)
		if currentIdx >= 0 {
			nextIdx := (currentIdx + delta + len(m.availableDevices)) % len(m.availableDevices)
			m.config.OutputDevice = m.availableDevices[nextIdx].ID
		}
	case 2: // Sample Rate
		rates := []int{22050, 44100, 48000, 96000}
		currentIdx := m.findIntIndex(rates, m.config.SampleRate)
		if currentIdx >= 0 {
			nextIdx := (currentIdx + delta + len(rates)) % len(rates)
			m.config.SampleRate = rates[nextIdx]
		}
	case 3: // Bit Depth
		depths := []int{16, 24, 32}
		currentIdx := m.findIntIndex(depths, m.config.BitDepth)
		if currentIdx >= 0 {
			nextIdx := (currentIdx + delta + len(depths)) % len(depths)
			m.config.BitDepth = depths[nextIdx]
		}
	case 4: // Channel Count
		channels := []int{1, 2}
		currentIdx := m.findIntIndex(channels, m.config.ChannelCount)
		if currentIdx >= 0 {
			nextIdx := (currentIdx + delta + len(channels)) % len(channels)
			m.config.ChannelCount = channels[nextIdx]
		}
	case 5: // Audio Format
		formats := []AudioFormat{FormatWAV, FormatMP3, FormatOGG}
		currentIdx := m.findFormatIndex(formats, m.config.DefaultFormat)
		if currentIdx >= 0 {
			nextIdx := (currentIdx + delta + len(formats)) % len(formats)
			m.config.DefaultFormat = formats[nextIdx]
		}
	case 6: // Volume
		currentVolume := m.getPlayerVolume()
		newVolume := currentVolume + float64(delta)*0.1
		if newVolume < 0.0 {
			newVolume = 0.0
		} else if newVolume > 1.0 {
			newVolume = 1.0
		}
		m.setPlayerVolume(newVolume)
	case 7: // Auto Normalize
		m.config.AutoNormalize = !m.config.AutoNormalize
	case 8: // Normalize Target
		m.config.NormalizeTarget += float64(delta) * 0.05
		if m.config.NormalizeTarget < 0.5 {
			m.config.NormalizeTarget = 0.5
		} else if m.config.NormalizeTarget > 1.0 {
			m.config.NormalizeTarget = 1.0
		}
	case 9: // Auto Trim Silence
		m.config.AutoTrimSilence = !m.config.AutoTrimSilence
	case 10: // Silence Threshold
		m.config.SilenceThreshold += float64(delta) * 0.005
		if m.config.SilenceThreshold < 0.001 {
			m.config.SilenceThreshold = 0.001
		} else if m.config.SilenceThreshold > 0.1 {
			m.config.SilenceThreshold = 0.1
		}
	case 11: // Clipping Threshold
		m.config.ClippingThreshold += float64(delta) * 0.05
		if m.config.ClippingThreshold < 0.8 {
			m.config.ClippingThreshold = 0.8
		} else if m.config.ClippingThreshold > 1.0 {
			m.config.ClippingThreshold = 1.0
		}
		// Update the clipping detector threshold
		if m.clippingDetector != nil {
			m.clippingDetector.threshold = m.config.ClippingThreshold
		}
	}
}

// Select setting (for device selection)
func (m *Model) selectSetting() {
	switch m.settingsSelectedIdx {
	case 0, 1: // Input/Output Device selection
		// Refresh available devices lazily then force fresh detection
		m.initializeAudioDevices()
		// Force a fresh detection by clearing and re-detecting
		m.config.AudioDevices = detectAudioDevices()
		m.availableDevices = m.config.AudioDevices
		setDefaultDevices(&m.config)
	default:
		// For other settings, no special action needed
	}
}

// Initialize audio devices - call this when actually needed
func (m *Model) initializeAudioDevices() {
	if len(m.config.AudioDevices) == 0 {
		log.Printf("Initializing audio devices...")
		m.config.AudioDevices = detectAudioDevices()
		m.availableDevices = m.config.AudioDevices
		setDefaultDevices(&m.config)

		// Save the updated config with detected devices
		if err := saveConfig(m.config); err != nil {
			log.Printf("Error saving config with audio devices: %v", err)
		}
	}
}

// Helper functions for finding indices
func (m *Model) findDeviceIndex(deviceID string) int {
	for i, device := range m.availableDevices {
		if device.ID == deviceID {
			return i
		}
	}
	return -1
}

func (m *Model) findIntIndex(slice []int, value int) int {
	for i, v := range slice {
		if v == value {
			return i
		}
	}
	return -1
}

func (m *Model) findFormatIndex(slice []AudioFormat, value AudioFormat) int {
	for i, v := range slice {
		if v == value {
			return i
		}
	}
	return -1
}

// Handle text input for renaming and tagging
func (m Model) handleTextInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch {
	case key.Matches(msg, keys.Enter):
		switch m.state {
		case StateRenaming:
			m.renameMemo(m.textInput.Value())
		case StateTagging:
			m.addTag(m.textInput.Value())
		}
		m.state = StateViewing
		m.textInput.Reset()

	case key.Matches(msg, keys.Escape), key.Matches(msg, keys.Quit):
		m.state = StateViewing
		m.textInput.Reset()

	default:
		m.textInput, cmd = m.textInput.Update(msg)
	}

	return m, cmd
}

// Handle main keyboard input
func (m Model) handleMainKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch {
	case key.Matches(msg, keys.Quit):
		return m, tea.Quit

	case key.Matches(msg, keys.Help):
		m.help.ShowAll = !m.help.ShowAll

	case key.Matches(msg, keys.Settings):
		m.state = StateSettings

	case key.Matches(msg, keys.TestFile):
		m.loadTestFile()

	case key.Matches(msg, keys.Record):
		if m.recording {
			m.stopRecording()
		} else {
			m.startRecording()
			cmds = append(cmds, recordingTick())
		}

	case key.Matches(msg, keys.Play):
		if len(m.memos) > 0 {
			if m.playing {
				m.pausePlayback()
			} else {
				m.startPlayback()
			}
		}

	case key.Matches(msg, keys.Stop):
		if m.playing {
			m.stopPlayback()
		}

	case key.Matches(msg, keys.Up), key.Matches(msg, keys.Down):
		// Let the list handle navigation
		var cmd tea.Cmd
		m.memoList, cmd = m.memoList.Update(msg)
		// Update our selected index to match the list
		if len(m.memoList.Items()) > 0 {
			m.selectedIdx = m.memoList.Index()
		}
		cmds = append(cmds, cmd)

	case key.Matches(msg, keys.Rename):
		if len(m.memos) > 0 {
			m.state = StateRenaming
			m.textInput.SetValue(m.memos[m.selectedIdx].Name)
			m.textInput.Focus()
		}

	case key.Matches(msg, keys.Tag):
		if len(m.memos) > 0 {
			m.state = StateTagging
			m.textInput.SetValue("")
			m.textInput.Focus()
		}

	case key.Matches(msg, keys.Delete):
		if len(m.memos) > 0 {
			m.deleteMemo()
		}

	case key.Matches(msg, keys.Export):
		if len(m.memos) > 0 {
			m.exportMemo()
		}

	case key.Matches(msg, keys.Escape):
		return m, tea.Quit
	}

	return m, tea.Batch(cmds...)
}

// Update waveform visualization (simulated)
func (m *Model) updateWaveform() {
	// Generate random waveform data for demo
	samples := make([]float32, 100)
	var max float32
	for i := range samples {
		val := float32(i%20-10) / 10.0
		samples[i] = val
		if val < 0 {
			val = -val
		}
		if val > max {
			max = val
		}
	}
	m.waveform = WaveformData{samples: samples, max: max}

	// Update VU meter
	m.vuMeter = VUMeterData{
		leftLevel:  0.7,
		rightLevel: 0.8,
	}
}

// Start recording
func (m *Model) startRecording() {
	// Initialize audio devices if not already done
	m.initializeAudioDevices()

	m.recording = true
	m.state = StateRecording
	m.recordingTime = 0
	m.lastUpdate = time.Now()

	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		log.Printf("Error initializing PortAudio: %v", err)
		m.stopRecording()
		return
	}

	// Find selected input device
	var inputDev *portaudio.DeviceInfo
	if m.config.InputDevice != "" {
		inputDev = getDeviceByID(m.config.InputDevice)
		log.Printf("Selected input device ID: %s", m.config.InputDevice)
		if inputDev != nil {
			log.Printf("Found input device: %s (channels: %d)", inputDev.Name, inputDev.MaxInputChannels)
		} else {
			log.Printf("Could not find input device with ID: %s", m.config.InputDevice)
		}
	}

	// Fallback to default input device
	if inputDev == nil {
		inputDev, _ = portaudio.DefaultInputDevice()
		log.Printf("Using default input device")
		if inputDev != nil {
			log.Printf("Default input device: %s (channels: %d)", inputDev.Name, inputDev.MaxInputChannels)
		}
	}

	if inputDev == nil {
		log.Printf("No input device available")
		m.stopRecording()
		return
	}

	// Create recording file
	filename := generateFilename(m.config.DefaultFormat)
	filePath := filepath.Join(m.config.MemosPath, filename)
	file, err := os.Create(filePath)
	if err != nil {
		log.Printf("Error creating recording file: %v", err)
		m.stopRecording()
		return
	}

	// Write WAV header (we'll update the data size later)
	if err := writeWAVHeader(file, m.config.SampleRate, m.config.ChannelCount, m.config.BitDepth*8, 0); err != nil {
		log.Printf("Error writing WAV header: %v", err)
		m.stopRecording()
		return
	}

	// Set up audio parameters - try to use device's preferred format
	params := portaudio.HighLatencyParameters(inputDev, nil)

	// Try to use device's preferred sample rate, fallback to config
	if inputDev.DefaultSampleRate > 0 {
		params.SampleRate = inputDev.DefaultSampleRate
		log.Printf("Using device's preferred sample rate: %.0f Hz", params.SampleRate)
	} else {
		params.SampleRate = float64(m.config.SampleRate)
		log.Printf("Using config sample rate: %.0f Hz", params.SampleRate)
	}

	// Try to use device's preferred channel count, fallback to config
	if inputDev.MaxInputChannels > 0 {
		// Use minimum of device max and our config
		channels := m.config.ChannelCount
		if inputDev.MaxInputChannels < channels {
			channels = inputDev.MaxInputChannels
		}
		params.Input.Channels = channels
		log.Printf("Using %d input channels (device max: %d, config: %d)",
			channels, inputDev.MaxInputChannels, m.config.ChannelCount)
	} else {
		params.Input.Channels = m.config.ChannelCount
		log.Printf("Using config channel count: %d", params.Input.Channels)
	}

	params.FramesPerBuffer = 1024

	// Create audio device
	m.audioDevice = &AudioDevice{
		recordingFile: file,
	}

	// Open input stream
	stream, err := portaudio.OpenStream(params, m.processAudioInput)
	if err != nil {
		log.Printf("Error opening recording stream: %v", err)
		m.stopRecording()
		return
	}

	m.audioDevice.stream = stream

	// Start recording
	if err := stream.Start(); err != nil {
		log.Printf("Error starting recording: %v", err)
		m.stopRecording()
	} else {
		log.Printf("Recording started successfully with device: %s", inputDev.Name)
	}
}

// Process audio input callback
func (m *Model) processAudioInput(in []int16) {
	// Write audio data to file (existing functionality)
	if m.audioDevice != nil && m.audioDevice.recordingFile != nil {
		if err := binary.Write(m.audioDevice.recordingFile, binary.LittleEndian, in); err != nil {
			log.Printf("Error writing audio data: %v", err)
		}
	}

	// NEW: Real-time waveform and clipping detection
	if m.audioAnalyzer != nil && m.clippingDetector != nil && len(in) > 0 {
		waveform, peak, _ := m.audioAnalyzer.ProcessSamples(in)
		m.realtimeWaveform = waveform
		m.peakLevel = peak

		m.clippingDetector.Analyze(in)
		m.isClipping = m.clippingDetector.IsClipping()

		// Update VU meter with real data
		if len(in) >= 2 {
			leftLevel := float32(in[0]) / 32768.0
			rightLevel := float32(in[1]) / 32768.0
			if leftLevel < 0 {
				leftLevel = -leftLevel
			}
			if rightLevel < 0 {
				rightLevel = -rightLevel
			}
			m.vuMeter = VUMeterData{
				leftLevel:  leftLevel,
				rightLevel: rightLevel,
			}
		}
	}

	// Legacy fallback for old waveform (kept for compatibility)
	if len(in) > 0 {
		samples := make([]float32, min(100, len(in)))
		var max float32
		for i := 0; i < len(samples) && i < len(in); i++ {
			val := float32(in[i]) / 32768.0
			samples[i] = val
			if val < 0 {
				val = -val
			}
			if val > max {
				max = val
			}
		}
		m.waveform = WaveformData{samples: samples, max: max}
	}
}

// Process audio output callback
func (m *Model) processAudioOutput(out []int16) {
	if m.audioDevice == nil || m.audioDevice.playbackData == nil {
		// Fill with silence if no data
		for i := range out {
			out[i] = 0
		}
		return
	}

	// Apply volume
	volume := m.config.Volume

	// Fill output buffer with audio data
	for i := range out {
		if m.audioDevice.playbackPos < len(m.audioDevice.playbackData) {
			// Apply volume and copy sample
			sample := float64(m.audioDevice.playbackData[m.audioDevice.playbackPos]) * volume
			if sample > 32767 {
				sample = 32767
			} else if sample < -32768 {
				sample = -32768
			}
			out[i] = int16(sample)
			m.audioDevice.playbackPos++
		} else {
			// End of audio data - fill with silence
			out[i] = 0
		}
	}

	// Note: End-of-playback detection is handled in the main thread (tick handler)
	// to avoid issues with stopping the stream from within the callback
}

// Stop recording and save memo
func (m *Model) stopRecording() {
	m.recording = false
	m.state = StateViewing

	var filename string
	var fileSize int64
	var duration float64

	// Clean up audio device and finalize recording
	if m.audioDevice != nil {
		// Stop and close the stream
		if m.audioDevice.stream != nil {
			if err := m.audioDevice.stream.Stop(); err != nil {
				log.Printf("Error stopping stream: %v", err)
			}
			if err := m.audioDevice.stream.Close(); err != nil {
				log.Printf("Error closing stream: %v", err)
			}
		}

		// Finalize the WAV file
		if m.audioDevice.recordingFile != nil {
			// Get file info
			fileInfo, _ := m.audioDevice.recordingFile.Stat()
			filename = fileInfo.Name()
			fileSize = fileInfo.Size()

			// Calculate actual duration
			// WAV file size minus header (44 bytes) divided by bytes per sample
			dataSize := fileSize - 44
			bytesPerSample := m.config.ChannelCount * m.config.BitDepth
			if bytesPerSample > 0 && m.config.SampleRate > 0 {
				samples := dataSize / int64(bytesPerSample)
				duration = float64(samples) / float64(m.config.SampleRate)
			} else {
				// Fallback calculation
				duration = m.recordingTime.Seconds()
			}

			// Update WAV header with correct data size
			if _, err := m.audioDevice.recordingFile.Seek(40, 0); err != nil {
				log.Printf("Error seeking in recording file: %v", err)
			}
			if err := binary.Write(m.audioDevice.recordingFile, binary.LittleEndian, uint32(dataSize)); err != nil {
				log.Printf("Error writing data size: %v", err)
			}

			// Close the file
			m.audioDevice.recordingFile.Close()
		}

		m.audioDevice = nil
	}

	// Terminate PortAudio
	if err := portaudio.Terminate(); err != nil {
		log.Printf("Error terminating PortAudio: %v", err)
	}

	// Create new memo with real data
	if filename != "" {
		// NEW: Apply post-processing
		filePath := filepath.Join(m.config.MemosPath, filename)
		processor := NewAudioProcessor(m.config.SilenceThreshold, m.config.NormalizeTarget)

		if err := processor.ProcessRecording(
			filePath,
			m.config.AutoTrimSilence,
			m.config.AutoNormalize,
		); err != nil {
			log.Printf("Error processing recording: %v", err)
		} else {
			log.Printf("Post-processing completed successfully")

			// Update file size after processing
			if info, err := os.Stat(filePath); err == nil {
				fileSize = info.Size()

				// Recalculate duration after processing
				if samples, sampleRate, channels, err := readWAVData(filePath); err == nil {
					if sampleRate > 0 {
						duration = float64(len(samples)) / float64(sampleRate) / float64(channels)
					}
				}
			}
		}

		memo := Memo{
			ID:       fmt.Sprintf("%d", time.Now().Unix()),
			Filename: filename,
			Name:     strings.TrimSuffix(filename, filepath.Ext(filename)),
			Duration: duration,
			Created:  time.Now(),
			Size:     fileSize,
			Tags:     []string{},
			Format:   m.config.DefaultFormat.String(),
		}

		// Add to memos list
		m.memos = append([]Memo{memo}, m.memos...)
		// Refresh list items to include the new memo
		m.memoList.SetItems(convertMemosToListItems(m.memos))

		// Save metadata
		if err := saveMemos(m.memos, m.config.MemosPath); err != nil {
			log.Printf("Error saving memos metadata: %v", err)
		}

		// Reset clipping detector for next recording
		if m.clippingDetector != nil {
			m.clippingDetector.Reset()
		}
	}

	// Reset recording data
	m.recordingTime = 0
}

// Start playback
func (m *Model) startPlayback() {
	if len(m.memos) == 0 {
		return
	}

	// Initialize audio devices if not already done
	m.initializeAudioDevices()

	memo := m.memos[m.selectedIdx]
	filePath := filepath.Join(m.config.MemosPath, memo.Filename)

	// Read WAV file data
	audioData, sampleRate, channels, err := readWAVData(filePath)
	if err != nil {
		log.Printf("Error reading audio file: %v", err)
		return
	}

	// Generate waveform data for playback visualization
	playbackAnalyzer := NewAudioAnalyzer(sampleRate, m.config.WaveformSampleRate)
	waveform, peak, _ := playbackAnalyzer.ProcessSamples(audioData)
	m.playbackWaveform = waveform
	m.playbackSampleRate = sampleRate
	m.peakLevel = peak
	log.Printf("Generated playback waveform: %d points, peak level: %.3f, sample rate: %d", len(waveform), peak, sampleRate)

	// Initialize PortAudio
	if err := portaudio.Initialize(); err != nil {
		log.Printf("Error initializing PortAudio: %v", err)
		return
	}

	// Find selected output device
	var outputDev *portaudio.DeviceInfo
	if m.config.OutputDevice != "" {
		outputDev = getDeviceByID(m.config.OutputDevice)
	}

	// Fallback to default output device
	if outputDev == nil {
		outputDev, _ = portaudio.DefaultOutputDevice()
	}

	if outputDev == nil {
		log.Printf("No output device available")
		if err := portaudio.Terminate(); err != nil {
			log.Printf("Error terminating PortAudio: %v", err)
		}
		return
	}

	// Set up audio parameters
	params := portaudio.HighLatencyParameters(nil, outputDev)
	params.SampleRate = float64(sampleRate)
	params.Output.Channels = channels
	params.FramesPerBuffer = 1024

	// Create audio device
	m.audioDevice = &AudioDevice{
		playbackData: audioData,
		playbackPos:  0,
	}

	// Open output stream
	stream, err := portaudio.OpenStream(params, m.processAudioOutput)
	if err != nil {
		log.Printf("Error opening playback stream: %v", err)
		if err := portaudio.Terminate(); err != nil {
			log.Printf("Error terminating PortAudio: %v", err)
		}
		return
	}

	m.audioDevice.stream = stream

	// Start playback
	if err := stream.Start(); err != nil {
		log.Printf("Error starting playback: %v", err)
		if err := portaudio.Terminate(); err != nil {
			log.Printf("Error terminating PortAudio: %v", err)
		}
		return
	}

	m.playing = true
	m.state = StatePlaying
	m.playbackPos = 0
	m.lastUpdate = time.Now()

	log.Printf("Playback started: %s", memo.Filename)
}

// Pause playback
func (m *Model) pausePlayback() {
	if m.audioDevice != nil && m.audioDevice.stream != nil {
		if err := m.audioDevice.stream.Stop(); err != nil {
			log.Printf("Error stopping playback stream: %v", err)
		}
	}
	m.playing = false
	m.state = StateViewing
	log.Printf("Playback paused")
}

// Stop playback
func (m *Model) stopPlayback() {
	if m.audioDevice != nil {
		// Stop and close the stream
		if m.audioDevice.stream != nil {
			if err := m.audioDevice.stream.Stop(); err != nil {
				log.Printf("Error stopping playback stream: %v", err)
			}
			if err := m.audioDevice.stream.Close(); err != nil {
				log.Printf("Error closing playback stream: %v", err)
			}
		}
		m.audioDevice = nil
	}

	// Terminate PortAudio
	if err := portaudio.Terminate(); err != nil {
		log.Printf("Error terminating PortAudio: %v", err)
	}

	m.playing = false
	m.state = StateViewing
	m.playbackPos = 0
	m.playbackWaveform = nil // Clear waveform data
	m.playbackSampleRate = 0 // Clear sample rate

	log.Printf("Playback stopped")
}

// Rename memo
func (m *Model) renameMemo(newName string) {
	if len(m.memos) > 0 && newName != "" {
		memo := &m.memos[m.selectedIdx]
		memo.Name = newName

		// Update in main memos list
		for i := range m.memos {
			if m.memos[i].ID == memo.ID {
				m.memos[i].Name = newName
				break
			}
		}

		// Refresh list items to reflect rename without resetting scroll elsewhere
		m.memoList.SetItems(convertMemosToListItems(m.memos))

		if err := saveMemos(m.memos, m.config.MemosPath); err != nil {
			log.Printf("Error saving memos metadata: %v", err)
		}
	}
}

// Add tag to memo
func (m *Model) addTag(tag string) {
	if len(m.memos) > 0 && tag != "" {
		memo := &m.memos[m.selectedIdx]

		// Check if tag already exists
		for _, existingTag := range memo.Tags {
			if existingTag == tag {
				return
			}
		}

		memo.Tags = append(memo.Tags, tag)

		// Update in main memos list
		for i := range m.memos {
			if m.memos[i].ID == memo.ID {
				m.memos[i].Tags = memo.Tags
				break
			}
		}

		// Refresh list items to reflect tag change
		m.memoList.SetItems(convertMemosToListItems(m.memos))

		if err := saveMemos(m.memos, m.config.MemosPath); err != nil {
			log.Printf("Error saving memos metadata: %v", err)
		}
	}
}

// Delete memo
func (m *Model) deleteMemo() {
	if len(m.memos) == 0 {
		return
	}

	memo := m.memos[m.selectedIdx]

	// Remove from file system
	filePath := filepath.Join(m.config.MemosPath, memo.Filename)
	os.Remove(filePath)

	// Remove from memos list
	for i, mem := range m.memos {
		if mem.ID == memo.ID {
			m.memos = append(m.memos[:i], m.memos[i+1:]...)
			break
		}
	}

	// Adjust selection
	if m.selectedIdx >= len(m.memos) {
		m.selectedIdx = len(m.memos) - 1
	}
	if m.selectedIdx < 0 {
		m.selectedIdx = 0
	}

	if err := saveMemos(m.memos, m.config.MemosPath); err != nil {
		log.Printf("Error saving memos metadata: %v", err)
	}

	// Refresh list items to reflect deletion without losing scroll position
	m.memoList.SetItems(convertMemosToListItems(m.memos))
}

// Export memo
func (m *Model) exportMemo() {
	if len(m.memos) == 0 {
		return
	}

	// Use the list's selected index instead of our stored index
	selectedIdx := m.memoList.Index()
	if selectedIdx >= len(m.memos) {
		selectedIdx = 0
	}

	memo := m.memos[selectedIdx]
	sourcePath := filepath.Join(m.config.MemosPath, memo.Filename)

	// Get user's home directory for export
	homeDir, _ := os.UserHomeDir()
	exportDir := filepath.Join(homeDir, "Downloads")

	// Create export filename with timestamp
	timestamp := time.Now().Format("2006-01-02_15-04-05")
	exportFilename := fmt.Sprintf("export_%s_%s", timestamp, memo.Filename)
	exportPath := filepath.Join(exportDir, exportFilename)

	// Copy file to export location
	log.Printf("Attempting to export memo: %s from %s to %s", memo.Name, sourcePath, exportPath)
	if err := copyFile(sourcePath, exportPath); err != nil {
		log.Printf("Export failed: %v", err)
		m.showNotification(fmt.Sprintf("Export failed: %v", err))
	} else {
		log.Printf("Export successful: %s", exportPath)
		m.showNotification(fmt.Sprintf("Exported to Downloads: %s", exportFilename))
	}
}

// Copy file helper function
func copyFile(src, dst string) error {
	sourceFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer sourceFile.Close()

	destFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = destFile.ReadFrom(sourceFile)
	return err
}

// Render the application
func (m Model) View() string {
	if m.width == 0 {
		return "Loading..."
	}

	switch m.state {
	case StateSettings:
		return m.renderSettings()
	default:
		return m.renderMain()
	}
}

// Render settings screen
func (m Model) renderSettings() string {
	var sections []string

	// Header
	sections = append(sections, titleStyle.Render(" VOICELOG SETTINGS "))

	// Settings list
	settings := []string{
		"Input Device:",
		"Output Device:",
		"Sample Rate:",
		"Bit Depth:",
		"Channels:",
		"Audio Format:",
		"Volume:",
		"Auto Normalize:",
		"Normalize Target:",
		"Auto Trim Silence:",
		"Silence Threshold:",
		"Clipping Threshold:",
	}

	values := []string{
		m.getDeviceName(m.config.InputDevice),
		m.getDeviceName(m.config.OutputDevice),
		fmt.Sprintf("%d Hz", m.config.SampleRate),
		fmt.Sprintf("%d-bit", m.config.BitDepth),
		fmt.Sprintf("%d", m.config.ChannelCount),
		m.config.DefaultFormat.String(),
		fmt.Sprintf("%.0f%%", m.getPlayerVolume()*100),
		boolToString(m.config.AutoNormalize),
		fmt.Sprintf("%.0f%%", m.config.NormalizeTarget*100),
		boolToString(m.config.AutoTrimSilence),
		fmt.Sprintf("%.1f%%", m.config.SilenceThreshold*100),
		fmt.Sprintf("%.0f%%", m.config.ClippingThreshold*100),
	}

	var lines []string
	for i, setting := range settings {
		var line string
		if i == m.settingsSelectedIdx {
			line += selectedStyle.Render("▶ ")
		} else {
			line += "  "
		}

		line += normalStyle.Render(setting)
		line += " "
		line += successStyle.Render(values[i])

		// Add arrows for navigation
		if i == m.settingsSelectedIdx {
			line += " " + mutedStyle.Render("← →")
		}

		lines = append(lines, line)
	}

	sections = append(sections, lipgloss.JoinVertical(lipgloss.Left, lines...))

	// System info
	sections = append(sections, "")
	sections = append(sections, mutedStyle.Render(getSystemAudioInfo()))

	// Instructions
	instructions := []string{
		"",
		"Navigation:",
		"  ↑/↓     Select setting",
		"  ←/→     Change value",
		"  ENTER   Refresh devices",
		"  ESC/q   Save and exit",
		"",
		"Press ESC/q to save settings and return...",
	}

	sections = append(sections, lipgloss.JoinVertical(lipgloss.Left, instructions...))

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// Get device name by ID
func (m Model) getDeviceName(deviceID string) string {
	log.Printf("Looking for device ID: %s", deviceID)
	log.Printf("Available devices: %d", len(m.availableDevices))

	for i, device := range m.availableDevices {
		log.Printf("Device %d: ID=%s, Name=%s, Input=%v, Output=%v",
			i, device.ID, device.Name, device.IsInput, device.IsOutput)
		if device.ID == deviceID {
			return device.Name
		}
	}
	return fmt.Sprintf("Unknown Device (ID: %s)", deviceID)
}

// Get system audio info
func getSystemAudioInfo() string {
	// Avoid initializing PortAudio here to prevent strict init/term cycles on some platforms
	return "Audio system: Ready"
}

// Get player volume (for settings display)
func (m Model) getPlayerVolume() float64 {
	return m.config.Volume
}

// Set player volume
func (m *Model) setPlayerVolume(volume float64) {
	if volume < 0.0 {
		volume = 0.0
	} else if volume > 1.0 {
		volume = 1.0
	}
	m.config.Volume = volume
}

// Render main interface
func (m Model) renderMain() string {
	var sections []string

	// Header
	sections = append(sections, m.renderHeader())

	// Check if we need to show audio visualizer
	showingVisualizer := m.recording || m.playing

	if showingVisualizer {
		// When recording/playing, show visualizer and compact main content
		sections = append(sections, m.renderAudioVisualizer())
		sections = append(sections, m.renderCompactMainContent())
	} else {
		// Normal view with full main content (memo list + speaker art)
		sections = append(sections, m.renderMainContent())
	}

	// Text input (for renaming/tagging)
	if m.state == StateRenaming || m.state == StateTagging {
		sections = append(sections, m.renderTextInput())
	}

	// Status bar
	sections = append(sections, m.renderStatusBar())

	return lipgloss.JoinVertical(lipgloss.Left, sections...)
}

// Render header
func (m Model) renderHeader() string {
	title := titleStyle.Render(" VOICELOG ")

	var status string
	switch m.state {
	case StateRecording:
		indicator := "●"
		if m.recordingPulse > 10 {
			indicator = " "
		}
		status = recordingStyle.Render(fmt.Sprintf("%s REC %s", indicator, formatDuration(m.recordingTime)))
	case StatePlaying:
		status = successStyle.Render("▶ PLAYING")
	default:
		if len(m.memos) == 1 {
			status = normalStyle.Render("1 memo")
		} else {
			status = normalStyle.Render(fmt.Sprintf("%d memos", len(m.memos)))
		}
	}

	// Create header content
	headerContent := lipgloss.JoinHorizontal(lipgloss.Top, title, " ", status)

	// Apply border styling
	return headerBorderStyle.Render(headerContent)
}

// Render audio visualizer
func (m Model) renderAudioVisualizer() string {
	var lines []string

	if m.recording {
		// NEW: Use real-time waveform
		waveformLine := "Waveform: "
		displaySamples := m.realtimeWaveform
		if len(displaySamples) == 0 && len(m.waveform.samples) > 0 {
			// Fallback to legacy waveform
			displaySamples = m.waveform.samples[:min(50, len(m.waveform.samples))]
		}

		for _, amplitude := range displaySamples {
			height := int(amplitude * 10)
			if height > 10 {
				height = 10
			}

			if height > 7 {
				waveformLine += "█"
			} else if height > 5 {
				waveformLine += "▆"
			} else if height > 3 {
				waveformLine += "▄"
			} else if height > 1 {
				waveformLine += "▂"
			} else {
				waveformLine += "·"
			}
		}
		lines = append(lines, waveformStyle.Render(waveformLine))

		// NEW: Peak level indicator
		if m.peakLevel > 0 {
			peakBar := fmt.Sprintf("Peak: [%s] %.1f%%",
				renderPeakBar(m.peakLevel, 20),
				m.peakLevel*100)
			lines = append(lines, normalStyle.Render(peakBar))
		}

		// NEW: Clipping indicator
		if m.isClipping && m.clippingDetector != nil {
			clipWarning := "⚠ CLIPPING DETECTED ⚠"
			// Flash effect
			if m.clippingDetector.flashCounter%4 < 2 {
				lines = append(lines,
					lipgloss.NewStyle().
						Foreground(lipgloss.Color("#FF0000")).
						Bold(true).
						Render(clipWarning))
			}
		}

		// VU Meters (existing)
		leftMeter := renderVUMeter("L", m.vuMeter.leftLevel)
		rightMeter := renderVUMeter("R", m.vuMeter.rightLevel)
		lines = append(lines, vuMeterStyle.Render(leftMeter))
		lines = append(lines, vuMeterStyle.Render(rightMeter))
	}

	if m.playing && len(m.memos) > 0 {
		// Render playback waveform
		waveformLine := "Waveform: "
		displaySamples := m.playbackWaveform
		if len(displaySamples) == 0 {
			waveformLine += "No waveform data"
		} else {
			// Calculate the portion of the waveform to display based on playback position
			memo := m.memos[m.selectedIdx]
			progress := m.playbackPos.Seconds() / memo.Duration
			if progress > 1 {
				progress = 1
			}

			windowSize := min(50, len(displaySamples)) // Display up to 50 points
			startIdx := int(progress * float64(max(0, len(displaySamples)-windowSize)))
			if startIdx < 0 {
				startIdx = 0
			}
			endIdx := startIdx + windowSize
			if endIdx > len(displaySamples) {
				endIdx = len(displaySamples)
			}

			for _, amplitude := range displaySamples[startIdx:endIdx] {
				height := int(amplitude * 10)
				if height > 10 {
					height = 10
				}
				if height > 7 {
					waveformLine += "█"
				} else if height > 5 {
					waveformLine += "▆"
				} else if height > 3 {
					waveformLine += "▄"
				} else if height > 1 {
					waveformLine += "▂"
				} else {
					waveformLine += "·"
				}
			}
		}
		lines = append(lines, waveformStyle.Render(waveformLine))

		// Timeline scrubber
		memo := m.memos[m.selectedIdx]
		progress := m.playbackPos.Seconds() / memo.Duration
		if progress > 1 {
			progress = 1
		}

		timeline := renderTimeline(progress, 50)
		timeDisplay := fmt.Sprintf("%s / %s",
			formatDuration(m.playbackPos),
			formatDuration(time.Duration(memo.Duration*float64(time.Second))))

		lines = append(lines, successStyle.Render(timeline))
		lines = append(lines, mutedStyle.Render(timeDisplay))
	}

	if len(lines) > 0 {
		// More compact - no extra spacing
		visualizerContent := lipgloss.JoinVertical(lipgloss.Left, lines...)
		return borderStyle.Render(visualizerContent)
	}

	return ""
}

// Render VU meter
func renderVUMeter(label string, level float32) string {
	barWidth := 30
	filled := int(level * float32(barWidth))

	bar := label + ": ["
	for i := 0; i < barWidth; i++ {
		if i < filled {
			if level > 0.8 {
				bar += "█"
			} else if level > 0.6 {
				bar += "▆"
			} else {
				bar += "▄"
			}
		}
	}
	return bar + "]"
}

// Render timeline scrubber
func renderTimeline(progress float64, width int) string {
	filled := int(progress * float64(width))
	timeline := "["
	for i := 0; i < width; i++ {
		if i < filled {
			timeline += "█"
		} else {
			timeline += "░"
		}
	}
	return timeline + "]"
}

// Render main content area with memo list and speaker art
func (m Model) renderMainContent() string {
	// Speaker ASCII art
	speakerArt := []string{
		"     ..:::::::..",
		"    //////\\\\\\\\\\\\\\",
		"    |||||||||||||",
		"    |||||||||||||",
		"    |||||||||||||",
		"HH  |||||||||||||   HH",
		"HH==================HH",
		"HH==================HH",
		"HH  #############   HH",
		"HH  #############   HH",
		"HH   ###########    HH",
		"HH    #########     HH",
		"HH     #######      HH",
		"HH      #####       HH",
		"HH        ()        HH",
		"\\\\         ()       //",
		"  \\\\       ()     //",
		"    \\\\      ()  //",
		"      \\\\     (//",
		"        \\\\  //)(",
		"      ____\\/___()",
		"  ,#################",
		" #####################",
		"~~~~~~~~~~~~~~~~~~~~~~~",
	}

	// Render memo list - always use the bordered list for consistent layout
	var memoListContent string
	fixedListWidth := 40 // Fixed width to prevent expansion

	if len(m.memos) == 0 {
		// Create empty list with placeholder message
		emptyList := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
		emptyList.Title = "MEMOS"
		emptyList.Styles.Title = titleStyle
		emptyList.SetShowHelp(false)
		listHeight := m.height - 15 // Reserve space for header, status, and help
		if listHeight < 10 {
			listHeight = 10 // Minimum height
		}
		emptyList.SetSize(fixedListWidth, listHeight)
		emptyList.SetFilteringEnabled(false) // Disable filtering

		// Add a placeholder item
		placeholderItem := list.Item(placeholderMemo{})
		emptyList.SetItems([]list.Item{placeholderItem})

		// Override the item count display to show 0 items
		emptyList.SetShowStatusBar(false) // Hide the status bar that shows item count
		memoListContent = memoListBorderStyle.Render(emptyList.View())
	} else {
		// Set full size for normal view
		listHeight := m.height - 15 // Reserve space for header, status, and help
		if listHeight < 10 {
			listHeight = 10 // Minimum height
		}
		m.memoList.SetSize(fixedListWidth, listHeight)
		m.memoList.SetShowStatusBar(true) // Show status bar for real items
		memoListContent = memoListBorderStyle.Render(m.memoList.View())
	}

	// Style the speaker art with two-tone colors
	speakerArtText := m.renderTwoToneSpeakerArt(speakerArt)

	// Add some spacing above the speaker art to align it better with the memo list
	speakerArtWithSpacing := lipgloss.JoinVertical(lipgloss.Left, "", speakerArtText)

	// Combine memo list and speaker art horizontally (memo list on left, speaker on right)
	return lipgloss.JoinHorizontal(lipgloss.Top, memoListContent, "    ", speakerArtWithSpacing)
}

// Render compact main content for when visualizer is active
func (m Model) renderCompactMainContent() string {
	// Show only the memo list without the ASCII art to save space
	var memoListContent string
	fixedListWidth := 60 // Wider list when no ASCII art

	if len(m.memos) == 0 {
		// Create empty list with placeholder message
		emptyList := list.New([]list.Item{}, list.NewDefaultDelegate(), 0, 0)
		emptyList.Title = "MEMOS"
		emptyList.Styles.Title = titleStyle
		emptyList.SetShowHelp(false)
		emptyList.SetSize(fixedListWidth, 8) // Shorter height to save vertical space
		emptyList.SetFilteringEnabled(false)

		// Add a placeholder item
		placeholderItem := list.Item(placeholderMemo{})
		emptyList.SetItems([]list.Item{placeholderItem})

		emptyList.SetShowStatusBar(false)
		memoListContent = memoListBorderStyle.Render(emptyList.View())
	} else {
		// Use existing memo list but with compact sizing
		m.memoList.SetSize(fixedListWidth, 8) // Shorter height
		m.memoList.SetShowStatusBar(true)
		memoListContent = memoListBorderStyle.Render(m.memoList.View())
	}

	return memoListContent
}

// Render two-tone speaker ASCII art
func (m Model) renderTwoToneSpeakerArt(speakerArt []string) string {
	// Define two-tone colors for the speaker
	primaryColor := lipgloss.NewStyle().Foreground(lipgloss.Color(PrimaryBlue))
	accentColor := lipgloss.NewStyle().Foreground(lipgloss.Color("#ee6ff8")) // Custom pink color

	var styledLines []string

	for i, line := range speakerArt {
		// Apply different colors to different parts of the speaker
		var styledLine string

		// Top part (grille) - use primary blue
		if i < 5 {
			styledLine = primaryColor.Render(line)
		} else if i >= 5 && i < 8 {
			// Mixed lines with HH and pipes - need to color them separately
			if strings.Contains(line, "|") && strings.Contains(line, "HH") {
				// Line 5: "HH  |||||||||||||   HH" - pipes should be primary blue, HH should be accent
				styledLine = m.colorMixedLineWithMultiple(line, map[string]lipgloss.Style{
					"|": primaryColor,
					"H": accentColor,
					"=": accentColor,
				})
			} else {
				// Pure HH and = sections - use accent cyan
				styledLine = accentColor.Render(line)
			}
		} else if i >= 8 && i < 14 {
			// Lines with # symbols and HH - need mixed coloring
			styledLine = m.colorMixedLineWithMultiple(line, map[string]lipgloss.Style{
				"#": primaryColor,
				"H": accentColor,
				"(": primaryColor,
				")": primaryColor,
			})
		} else if i == 14 {
			// Line with () symbols and HH - use mixed coloring
			styledLine = m.colorMixedLineWithMultiple(line, map[string]lipgloss.Style{
				"H": accentColor,
				"(": accentColor,
				")": accentColor,
			})
		} else {
			// Bottom section (\\, ____, #####, ~~~~) - use accent cyan
			styledLine = accentColor.Render(line)
		}

		styledLines = append(styledLines, styledLine)
	}

	return lipgloss.JoinVertical(lipgloss.Left, styledLines...)
}

// Helper function to color multiple specific characters in a line
func (m Model) colorMixedLineWithMultiple(line string, charColors map[string]lipgloss.Style) string {
	var result strings.Builder
	defaultColor := lipgloss.NewStyle().Foreground(lipgloss.Color(TextMuted))

	for _, char := range line {
		charStr := string(char)
		if style, exists := charColors[charStr]; exists {
			result.WriteString(style.Render(charStr))
		} else {
			result.WriteString(defaultColor.Render(charStr))
		}
	}

	return result.String()
}

// Render text input
func (m Model) renderTextInput() string {
	var prompt string
	switch m.state {
	case StateRenaming:
		prompt = "New name: "
	case StateTagging:
		prompt = "Add tag: "
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		"",
		normalStyle.Render(prompt)+m.textInput.View(),
		"",
	)
}

// Render status bar
func (m Model) renderStatusBar() string {
	var status string

	switch {
	case m.recording:
		status = recordingStyle.Render("● RECORDING")
	case m.playing:
		status = successStyle.Render("▶ PLAYING")
	default:
		status = normalStyle.Render("Ready")
	}

	// Create status line
	statusLine := status

	// Add notification if present
	if m.notification != "" {
		statusLine += " | " + successStyle.Render(m.notification)
	}

	// Use bubbles help component for commands
	helpView := m.help.View(keys)

	// Join status and help on separate lines
	statusBar := lipgloss.JoinVertical(lipgloss.Left,
		statusBarStyle.Render(statusLine),
		helpView,
	)

	return statusBar
}

// ============================================================================
// AUDIO PROCESSING IMPLEMENTATIONS
// ============================================================================

// NewAudioAnalyzer creates a new audio analyzer
func NewAudioAnalyzer(sampleRate, waveformPoints int) *AudioAnalyzer {
	// Validate inputs to prevent divide by zero
	if sampleRate <= 0 {
		sampleRate = SampleRate // Use default sample rate
	}
	if waveformPoints <= 0 {
		waveformPoints = 100 // Use default waveform points
	}

	// Buffer size: 1 second of audio
	bufferSize := sampleRate * 2 // Stereo
	downsampleRatio := bufferSize / waveformPoints
	if downsampleRatio < 1 {
		downsampleRatio = 1
	}

	log.Printf("AudioAnalyzer initialized: sampleRate=%d, waveformPoints=%d, bufferSize=%d, downsampleRatio=%d",
		sampleRate, waveformPoints, bufferSize, downsampleRatio)

	return &AudioAnalyzer{
		buffer:          make([]int16, bufferSize),
		bufferSize:      bufferSize,
		writePos:        0,
		downsampleRatio: downsampleRatio,
		waveformPoints:  waveformPoints,
	}
}

// ProcessSamples processes incoming audio samples and returns waveform data
func (a *AudioAnalyzer) ProcessSamples(samples []int16) (waveform []float32, peak float32, isClipping bool) {
	waveform = make([]float32, a.waveformPoints)
	peak = 0.0
	isClipping = false

	// Add samples to circular buffer
	for _, sample := range samples {
		a.buffer[a.writePos] = sample
		a.writePos = (a.writePos + 1) % a.bufferSize

		// Check for clipping
		absSample := math.Abs(float64(sample)) / 32768.0
		if absSample > float64(peak) {
			peak = float32(absSample)
		}
		if absSample >= 0.95 {
			isClipping = true
		}
	}

	// Downsample buffer to create waveform visualization
	for i := 0; i < a.waveformPoints; i++ {
		startIdx := i * a.downsampleRatio
		endIdx := startIdx + a.downsampleRatio

		// Calculate RMS for this segment
		var sumSquares float64
		count := 0

		for j := startIdx; j < endIdx && j < a.bufferSize; j++ {
			idx := (a.writePos + j) % a.bufferSize
			normalized := float64(a.buffer[idx]) / 32768.0
			sumSquares += normalized * normalized
			count++
		}

		if count > 0 {
			rms := math.Sqrt(sumSquares / float64(count))
			waveform[i] = float32(rms)
		}
	}

	return waveform, peak, isClipping
}

// NewAudioProcessor creates a new audio processor
func NewAudioProcessor(silenceThreshold, normalizeTarget float64) *AudioProcessor {
	return &AudioProcessor{
		SilenceThreshold: silenceThreshold,
		NormalizeTarget:  normalizeTarget,
	}
}

// TrimSilence removes silence from the beginning and end of audio data
func (ap *AudioProcessor) TrimSilence(samples []int16, sampleRate, channels int) []int16 {
	if len(samples) == 0 {
		return samples
	}

	frameSize := channels
	numFrames := len(samples) / frameSize

	// Find start of non-silent audio
	startFrame := 0
	for i := 0; i < numFrames; i++ {
		frameStart := i * frameSize
		frameEnd := frameStart + frameSize
		if frameEnd > len(samples) {
			break
		}

		if ap.isFrameSilent(samples[frameStart:frameEnd]) {
			startFrame = i + 1
		} else {
			break
		}
	}

	// Find end of non-silent audio
	endFrame := numFrames
	for i := numFrames - 1; i >= startFrame; i-- {
		frameStart := i * frameSize
		frameEnd := frameStart + frameSize
		if frameEnd > len(samples) {
			continue
		}

		if ap.isFrameSilent(samples[frameStart:frameEnd]) {
			endFrame = i
		} else {
			break
		}
	}

	// Extract non-silent portion
	if startFrame >= endFrame {
		// Entire audio is silent, return small buffer
		return samples[:min(frameSize*10, len(samples))]
	}

	startSample := startFrame * frameSize
	endSample := endFrame * frameSize

	log.Printf("Trimmed silence: start=%d, end=%d (total=%d frames)",
		startFrame, endFrame, numFrames)

	return samples[startSample:endSample]
}

// isFrameSilent checks if an audio frame is below the silence threshold
func (ap *AudioProcessor) isFrameSilent(frame []int16) bool {
	var sumSquares float64

	for _, sample := range frame {
		normalized := float64(sample) / 32768.0
		sumSquares += normalized * normalized
	}

	rms := math.Sqrt(sumSquares / float64(len(frame)))
	return rms < ap.SilenceThreshold
}

// Normalize adjusts audio amplitude to reach target peak level
func (ap *AudioProcessor) Normalize(samples []int16) []int16 {
	if len(samples) == 0 {
		return samples
	}

	// Find current peak
	var maxAbs int16
	for _, sample := range samples {
		abs := sample
		if abs < 0 {
			abs = -abs
		}
		if abs > maxAbs {
			maxAbs = abs
		}
	}

	if maxAbs == 0 {
		return samples
	}

	// Calculate gain to reach target
	currentPeak := float64(maxAbs) / 32768.0
	targetPeak := ap.NormalizeTarget
	gain := targetPeak / currentPeak

	// Don't amplify if already at or above target
	if gain <= 1.0 {
		log.Printf("Audio already normalized (peak=%.2f)", currentPeak)
		return samples
	}

	log.Printf("Normalizing audio: gain=%.2fx (peak %.2f -> %.2f)",
		gain, currentPeak, targetPeak)

	// Apply gain
	normalized := make([]int16, len(samples))
	for i, sample := range samples {
		amplified := float64(sample) * gain

		// Clamp to prevent overflow
		if amplified > 32767 {
			amplified = 32767
		} else if amplified < -32768 {
			amplified = -32768
		}

		normalized[i] = int16(amplified)
	}

	return normalized
}

// ProcessRecording applies all post-processing to a recorded audio file
func (ap *AudioProcessor) ProcessRecording(filePath string, autoTrim, autoNormalize bool) error {
	// Read WAV file
	samples, sampleRate, channels, err := readWAVData(filePath)
	if err != nil {
		return fmt.Errorf("failed to read WAV: %w", err)
	}

	log.Printf("Processing recording: %d samples, %d Hz, %d channels",
		len(samples), sampleRate, channels)

	// Apply silence trimming
	if autoTrim {
		samples = ap.TrimSilence(samples, sampleRate, channels)
	}

	// Apply normalization
	if autoNormalize {
		samples = ap.Normalize(samples)
	}

	// Write processed audio back to file
	return ap.writeProcessedWAV(filePath, samples, sampleRate, channels)
}

// writeProcessedWAV writes processed audio data to a WAV file
func (ap *AudioProcessor) writeProcessedWAV(filePath string, samples []int16, sampleRate, channels int) error {
	file, err := os.Create(filePath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	// Write WAV header
	dataSize := int64(len(samples) * 2) // 2 bytes per sample (int16)
	if err := writeWAVHeader(file, sampleRate, channels, 16, dataSize); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Write sample data
	for _, sample := range samples {
		if err := binary.Write(file, binary.LittleEndian, sample); err != nil {
			return fmt.Errorf("failed to write sample: %w", err)
		}
	}

	return nil
}

// NewClippingDetector creates a new clipping detector
func NewClippingDetector(threshold float64) *ClippingDetector {
	return &ClippingDetector{
		threshold: threshold,
	}
}

// Analyze checks samples for clipping
func (cd *ClippingDetector) Analyze(samples []int16) {
	for _, sample := range samples {
		cd.totalSamples++
		normalized := math.Abs(float64(sample)) / 32768.0

		if normalized >= cd.threshold {
			cd.isClipping = true
			cd.clipCount++
			cd.flashCounter = 20 // Flash for 20 frames
		}
	}
}

// IsClipping returns true if clipping is detected
func (cd *ClippingDetector) IsClipping() bool {
	return cd.isClipping && cd.flashCounter > 0
}

// UpdateFlash updates the flash counter for visual indicator
func (cd *ClippingDetector) UpdateFlash() {
	if cd.flashCounter > 0 {
		cd.flashCounter--
	}
	if cd.flashCounter == 0 {
		cd.isClipping = false
	}
}

// GetClipPercentage returns percentage of clipped samples
func (cd *ClippingDetector) GetClipPercentage() float64 {
	if cd.totalSamples == 0 {
		return 0.0
	}
	return (float64(cd.clipCount) / float64(cd.totalSamples)) * 100.0
}

// Reset resets the detector state
func (cd *ClippingDetector) Reset() {
	cd.isClipping = false
	cd.clipCount = 0
	cd.totalSamples = 0
	cd.flashCounter = 0
}

// Helper functions for UI display
func boolToString(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func renderPeakBar(level float32, width int) string {
	filled := int(level * float32(width))
	bar := ""

	for i := 0; i < width; i++ {
		if i < filled {
			if level > 0.95 {
				bar += "█" // Red zone
			} else if level > 0.8 {
				bar += "▆" // Yellow zone
			} else {
				bar += "▄" // Green zone
			}
		} else {
			bar += "░"
		}
	}

	return bar
}

// Main function
func main() {
	setupLogging()
	log.Printf("Starting voicelog application")

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		log.Printf("Error running voicelog: %v", err)
		fmt.Printf("Error running voicelog: %v\n", err)
		os.Exit(1)
	}
}

package main

import (
	"crypto/rand"
	"fmt"
	"log"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
  "io"

	"btcgo/src/crypto/btc_utils"

	"github.com/dustin/go-humanize"
	"github.com/fatih/color"
)

// Wallets struct to hold the array of wallet addresses
type Wallets struct {
	Addresses [][]byte `json:"wallets"`
}

// Range struct to hold the minimum, maximum, and status
type Range struct {
	Min    string `json:"min"`
	Max    string `json:"max"`
	Status int    `json:"status"`
}

// Ranges struct to hold an array of ranges
type Ranges struct {
	Ranges []Range `json:"ranges"`
}

var (
	testedKeys   []*big.Int
	foundAddress *big.Int
	mu           sync.Mutex
	keysPerSecond  int64 // Global variable to store keys per second
)

func main() {
    green := color.New(color.FgGreen).SprintFunc()

    exePath, err := os.Executable()
    if (err != nil) {
        fmt.Printf("Erro ao obter o caminho do executável: %v\n", err)
        return
    }
    rootDir := filepath.Dir(exePath)

    ranges, err := LoadRanges(filepath.Join(rootDir, "data", "ranges.json"))
    if err != nil {
        log.Fatalf("Failed to load ranges: %v", err)
    }

    color.Cyan("BTC GO - Investidor Internacional")
    color.White("v0.2")

    // Ask the user for the range number
    rangeNumber := PromptRangeNumber(len(ranges.Ranges))
    //rangeNumber := 66

    // Initialize min and max values of the selected range
    minKeyHex := ranges.Ranges[rangeNumber-1].Min
    maxKeyHex := ranges.Ranges[rangeNumber-1].Max

    minKeyInt := new(big.Int)
    maxKeyInt := new(big.Int)

    minKeyInt.SetString(minKeyHex[2:], 16)
    maxKeyInt.SetString(maxKeyHex[2:], 16)

    // Load wallet addresses from JSON file
    wallets, err := LoadWallets(filepath.Join(rootDir, "data", "wallets.json"))
    if err != nil {
        log.Fatalf("Failed to load wallets: %v", err)
    }

    keysChecked := 0
    startTime := time.Now()

    // Number of CPU cores to use
    numCPU := runtime.NumCPU()
    fmt.Printf("CPUs detectados: %s\n", green(numCPU))
    runtime.GOMAXPROCS(numCPU * 2)

    // Create a channel to send private keys to workers
    privKeyChan := make(chan *big.Int)
    // Create a channel to receive results from workers
    resultChan := make(chan *big.Int)
    // Create a wait group to wait for all workers to finish
    var wg sync.WaitGroup

    // Create a channel to signal workers to stop
    stopChan := make(chan struct{})

    // Start worker goroutines
    for i := 0; i < numCPU*2; i++ {
        wg.Add(1)
        go worker(wallets, privKeyChan, resultChan, stopChan, &wg)
    }

    // Ticker for periodic updates every 5 seconds
    ticker := time.NewTicker(5 * time.Second)
    done := make(chan bool)

    // Goroutine to print speed updates
    go func() {
        for {
            select {
            case <-ticker.C:
                elapsedTime := time.Since(startTime).Seconds()
                keysPerSecond := float64(keysChecked) / elapsedTime
                fmt.Printf("Chaves checadas: %s, Chaves por segundo: %s\n", humanize.Comma(int64(keysChecked)), humanize.Comma(int64(keysPerSecond)))
            case <-done:
                ticker.Stop()
                return
            }
        }
    }()

    // Ticker for saving the tested keys every minute
    saveTicker := time.NewTicker(10 * time.Second)

    go func() {
        for {
            select {
            case <-saveTicker.C:
                elapsedTime := time.Since(startTime).Seconds()
                keysPerSecond := float64(keysChecked) / elapsedTime
                err := saveKeysToHTML(rootDir, keysPerSecond)
                if err != nil {
                    log.Printf("Erro ao salvar as chaves testadas: %v", err)
                }
            case <-done:
                saveTicker.Stop()
                return
            }
        }
    }()

    // Send random private keys to the workers
    go func() {
        for {
            privKeyInt, err := rand.Int(rand.Reader, new(big.Int).Sub(maxKeyInt, minKeyInt))
            if err != nil {
                log.Printf("Erro ao gerar chave privada: %v", err)
                continue
            }
            privKeyInt.Add(privKeyInt, minKeyInt)

            select {
            case privKeyChan <- privKeyInt:
                mu.Lock()
                testedKeys = append(testedKeys, privKeyInt)
                if len(testedKeys) > 10 {
                    testedKeys = testedKeys[len(testedKeys)-10:]
                }
                mu.Unlock()
                keysChecked++
            case <-stopChan:
                close(privKeyChan)
                return
            }
        }
    }()

    // Wait for a result from any worker
    select {
    case foundAddress = <-resultChan:
        color.Yellow("Chave privada encontrada: %064x\n", foundAddress)
        color.Yellow("WIF: %s", btc_utils.GenerateWif(foundAddress))
        close(stopChan)
    }

    // Wait for all workers to finish
    wg.Wait()

    // Save the tested keys and the found key before exiting
    elapsedTime := time.Since(startTime).Seconds()
    keysPerSecond := float64(keysChecked) / elapsedTime
    err = saveKeysToHTML(rootDir, keysPerSecond)
    if err != nil {
        log.Printf("Erro ao salvar as chaves testadas: %v", err)
    }

    fmt.Printf("Chaves checadas: %s\n", humanize.Comma(int64(keysChecked)))
    fmt.Printf("Tempo: %.2f seconds\n", elapsedTime)
    fmt.Printf("Chaves por segundo: %s\n", humanize.Comma(int64(keysPerSecond)))
}


func worker(wallets *Wallets, privKeyChan <-chan *big.Int, resultChan chan<- *big.Int, stopChan <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	for {
		select {
		case privKeyInt, ok := <-privKeyChan:
			if !ok {
				return
			}
			address := btc_utils.CreatePublicHash160(privKeyInt)
			if Contains(wallets.Addresses, address) {
				resultChan <- privKeyInt
				return
			}
		case <-stopChan:
			return
		}
	}
}

func saveKeysToHTML(rootDir string, keysPerSecond float64) error {
    mu.Lock()
    defer mu.Unlock()

    filePath := filepath.Join(rootDir, "index.html")
    file, err := os.Create(filePath) // Create or truncate the file
    if err != nil {
        return err
    }
    defer file.Close()

    // HTML header with CSS link and meta tags
    _, err = file.WriteString(`
    <!DOCTYPE html>
    <html lang="en">
    <head>
        <meta charset="UTF-8">
        <meta name="viewport" content="width=device-width, initial-scale=1.0">
        <meta http-equiv="refresh" content="30">
        <title>Keys Report</title>
        <link rel="stylesheet" type="text/css" href="styles.css">
    </head>
    <body>
    <div class="container">
    `)
    if err != nil {
        return err
    }

    // Found key, if any
    if foundAddress != nil {
        _, err = file.WriteString(`<div class="found-key"><h1>Chave Encontrada</h1>`)
        if err != nil {
            return err
        }
        _, err = file.WriteString(fmt.Sprintf("<p>Chave privada encontrada: %064x</p>", foundAddress))
        if err != nil {
            return err
        }
        _, err = file.WriteString(fmt.Sprintf(`<p id="wif">WIF: %s</p></div>`, btc_utils.GenerateWif(foundAddress)))
        if err != nil {
            return err
        }
    }

    // Tested keys
    _, err = file.WriteString(`<div class="tested-keys"><h1>Últimas 10 chaves testadas</h1><ul>`)
    if err != nil {
        return err
    }

    for _, key := range testedKeys {
        _, err = file.WriteString(fmt.Sprintf("<li>%x</li>", key))
        if err != nil {
            return err
        }
    }

    _, err = file.WriteString("</ul>")
    if err != nil {
        return err
    }

	  // Keys per second
	  _, err = file.WriteString(fmt.Sprintf("<div class=\"keys-per-second\"><p>Chaves por segundo: %s</p></div></div>", humanize.Comma(int64(keysPerSecond))))
	  if err != nil {
		  return err
	  }


    // HTML footer
    _, err = file.WriteString(`
    </div>
    </body>
    </html>
    `)
    if err != nil {
        return err
    }

    // Após criar e salvar o arquivo index.html
    err = file.Sync() // Garantir que todos os dados foram escritos no arquivo
    if err != nil {
        return err
    }

    // Caminho para a cópia do arquivo
    destPath := "/var/www/html/index.html"

    // Copiar o arquivo para /var/www/html
    input, err := os.Open(filePath)
    if err != nil {
        return err
    }
    defer input.Close()

    output, err := os.Create(destPath)
    if err != nil {
        return err
    }
    defer output.Close()

    _, err = io.Copy(output, input)
    if err != nil {
        return err
    }

    // Garantir que todos os dados foram escritos no arquivo de destino
    err = output.Sync()
    if err != nil {
        return err
    }

 return nil
}
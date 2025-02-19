//go:generate goversioninfo

package main

import (
    "fmt"
    "log"
    "os"
    "flag"
    "sync"
    "time"
    "path/filepath"

    "github.com/gin-gonic/gin"
    "github.com/rjeczalik/notify" // for live-reload of manifests

    "rewinged/models"
    "rewinged/controllers"
)

// These variables are overwritten at compile/link time using -ldflags
var version = "development-build"
var commit = "unknown"
var compileTime = "unknown"
var releaseMode = "false"

var wg sync.WaitGroup
var jobs chan string = make(chan string)

func main() {
    versionFlagPtr := flag.Bool("version", false, "Print the version information and exit")
    packagePathPtr := flag.String("manifestPath", "./packages", "The directory to search for package manifest files")

    tlsEnablePtr := flag.Bool("https", false, "Serve encrypted HTTPS traffic directly from rewinged without the need for a proxy")
    tlsCertificatePtr := flag.String("httpsCertificateFile", "./cert.pem", "The webserver certificate to use if HTTPS is enabled")
    tlsPrivateKeyPtr := flag.String("httpsPrivateKeyFile", "./private.key", "The private key file to use if HTTPS is enabled")
    listenAddrPtr := flag.String("listen", "localhost:8080", "The address and port for the REST API to listen on")

    flag.Parse()

    if *versionFlagPtr {
        fmt.Printf("rewinged %v\n\ncommit:\t\t%v\ncompiled:\t%v\n", version, commit, compileTime)
        os.Exit(0)
    }

    fmt.Println("Searching for manifests...")
    // Start up 10 worker goroutines that can parse in manifest-files from one directory each
    for w := 1; w <= 6; w++ {
        go ingestManifestsWorker()
    }

    getManifests(*packagePathPtr)
    wg.Wait()

    // I don't know whether this is safe.
    // if manifests is just a reference-copy of manifests2 then it wouldn't be I think?
    // But *currently* since live-reload isn't implemented yet, manifests2 won't be written
    // to after this point so it's safe for now - TODO: only access manifests2 in a thread-safe way
    fmt.Println("Found", models.Manifests.GetManifestCount(), "package manifests.")

    fmt.Println("Watching manifestPath for changes ...")
    // Make the channel buffered to try and not miss events. Notify will drop
    // an event if the receiver is not able to keep up the sending pace.
    fileEventsBuffer := 100
    fileEventsChannel := make(chan notify.EventInfo, fileEventsBuffer)

    // Recursively listen for Create and Write events in the manifestPath.
    // Currently not watching for remove / delete events because we couldn't
    // correlate filenames to packages anyway so there's no way to know which
    // package is affected by the event.
    if err := notify.Watch(*packagePathPtr + "/...", fileEventsChannel, notify.Create, notify.Write); err != nil {
        log.Fatal(err)
    }
    defer notify.Stop(fileEventsChannel)

    // If an event is received, push its directory-path to the jobs channel
    go func() {
        for {
            // Detect and handle channel overflow
            // This is a loop because it is possible for the channel to fill up
            // multiple times in a row if events are flooding in for a prolonged
            // period of time, thus necessitating further full rescans
            for len(fileEventsChannel) == fileEventsBuffer {
                // If the channel is ever full we are missing events as the notify package drops them at this point
                log.Println("\x1b[31mfileEventsChannel full - we're missing events - will perform full manifest rescan\x1b[0m")
                // Wait out the thundering herd - events have been lost anyway
                time.Sleep(5 * time.Second)
                // Drop all events to clear the channel, this also enables new events to stream in again
                CLEAR_CHANNEL: for { select { case <- fileEventsChannel:; default: break CLEAR_CHANNEL } }
                getManifests(*packagePathPtr)
                // wait for the synchronous full rescan to finish.
                // any events accumulated in the meantime will be processed after.
                wg.Wait()
            }

            ei := <- fileEventsChannel
            log.Printf("Received event (type %T):\n\t%+v\n", ei, ei)
            wg.Add(1)
            jobs <- filepath.Dir(ei.Path())
        }
    }()

    if releaseMode == "true" {
        gin.SetMode(gin.ReleaseMode)
    }
    router := gin.Default()
    router.SetTrustedProxies(nil)
    router.GET("/information", controllers.GetInformation)
    router.GET("/packages", controllers.GetPackages)
    router.POST("/manifestSearch", controllers.SearchForPackage)
    router.GET("/packageManifests/:package_identifier", controllers.GetPackage)

    fmt.Println("Starting server on", *listenAddrPtr)
    if *tlsEnablePtr {
        if err := router.RunTLS(*listenAddrPtr, *tlsCertificatePtr, *tlsPrivateKeyPtr); err != nil {
            log.Fatal("error could not start webserver:", err)
        }
    } else {
        if err := router.Run(*listenAddrPtr); err != nil {
            log.Fatal("error could not start webserver:", err)
        }
    }
}

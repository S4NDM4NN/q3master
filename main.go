package main

func main() {
	go startMasterServer()
	go startServerPoller()
	startUpstreamDiscovery()
	startWebServer()
}

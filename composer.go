package composer

import (
	"context"
	"errors"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

type clientEnv struct {
	ConnectionCloseTimeout int
	IdleConnectionTimeout  int
	StartupRetries         int
	StartupRetryDelay      int
	StartupDelay           int
	CycleTime              int
	Port                   string
}

type config struct {
	clientEnv     *clientEnv
	AvoidNetworks map[string]string
	AvoidMasters  int
}

func getConfig() (*config, error) {

	// first build client environment
	cconfig := &config{clientEnv: &clientEnv{}}

	timeoutString := os.Getenv("STARTUP_RETRIES")
	if timeoutString != "" {
		s, err := strconv.Atoi(timeoutString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for STARTUP_RETRIES: " + err.Error())
		}
		cconfig.clientEnv.StartupRetries = s
	} else {
		cconfig.clientEnv.StartupRetries = 1
	}

	timeoutString = os.Getenv("STARTUP_DELAY_SECONDS")
	if timeoutString != "" {
		s, err := strconv.Atoi(timeoutString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for STARTUP_DELAY_SECONDS: " + err.Error())
		}
		cconfig.clientEnv.StartupDelay = s
	} else {
		cconfig.clientEnv.StartupDelay = 1
	}

	timeoutString = os.Getenv("STARTUP_RETRIES_DELAY_SECONDS")
	if timeoutString != "" {
		s, err := strconv.Atoi(timeoutString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for STARTUP_RETRIES_DELAY_SECONDS: " + err.Error())
		}
		cconfig.clientEnv.StartupRetryDelay = s
	} else {
		cconfig.clientEnv.StartupRetryDelay = 1
	}

	timeoutString = os.Getenv("CONNECTION_TIMEOUT_SECONDS")
	if timeoutString != "" {
		s, err := strconv.Atoi(timeoutString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for CONNECTION_TIMEOUT_SECONDS: " + err.Error())
		}
		cconfig.clientEnv.ConnectionCloseTimeout = s
	} else {
		cconfig.clientEnv.ConnectionCloseTimeout = 1
	}

	timeoutString = os.Getenv("IDLE_CONNECTION_TIMEOUT_SECONDS")
	if timeoutString != "" {
		s, err := strconv.Atoi(timeoutString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for IDLE_CONNECTION_TIMEOUT_SECONDS: " + err.Error())
		}
		cconfig.clientEnv.IdleConnectionTimeout = s
	} else {
		cconfig.clientEnv.IdleConnectionTimeout = 1
	}

	timeoutString = os.Getenv("CYCLE_TIME_SECONDS")
	if timeoutString != "" {
		s, err := strconv.Atoi(timeoutString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for CYCLE_TIME_SECONDS: " + err.Error())
		}
		cconfig.clientEnv.CycleTime = s
	} else {
		cconfig.clientEnv.CycleTime = 10
	}

	portString := os.Getenv("PORT")
	if portString != "" {
		cconfig.clientEnv.Port = portString
	} else {
		cconfig.clientEnv.Port = "8111"
	}

	// now build our composer config

	avoidStrings := os.Getenv("AVOID_NETWORKS")
	if avoidStrings != "" {
		nets := getSubStringsMap(avoidStrings)
		if len(nets) == 0 {
			return cconfig, errors.New("invalid value passed for AVOID_NETWORKS")
		}
		cconfig.AvoidNetworks = nets
	} else {
		// not specified, so set to default
		cconfig.AvoidNetworks = map[string]string{"ingress": "ingress"}
	}

	avoidMastersString := os.Getenv("AVOID_MASTERS")
	if avoidMastersString != "" {
		s, err := strconv.Atoi(avoidMastersString)
		if err != nil {
			return cconfig, errors.New("invalid value passed for AVOID_INGRESS: " + err.Error())
		}
		cconfig.AvoidMasters = s
	} else {
		// not specified, so set to default
		cconfig.AvoidMasters = 1
	}

	return cconfig, nil
}

func getSubStringsMap(array string) map[string]string {
	// simple helper that splits string by comma, and returns map
	result := make(map[string]string)
	list := strings.Split(array, ",")
	for _, v := range list {
		result[v] = v
	}
	return result
}

func getNetworkList(avoidNetworks map[string]string) []string {
	cli, err := client.NewClient("unix:///var/run/docker.sock", "", nil, nil)
	if err != nil {
		panic(err)

	}

	ctx := context.Background()

	list, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		log.Fatalf("docker api returned an error: %s\n", err.Error())
	}
	networks := []string{}

	for _, network := range list {
		if network.Driver == "overlay" {
			// if NOT in list of networks to avoid, add it to our worklist
			if _, present := avoidNetworks[network.Name]; !present {
				networks = append(networks, network.Name)
			}
		}
	}
	return networks
}

func getNodeList(avoidMasters int) []string {
	cli, err := client.NewClient("unix:///var/run/docker.sock", "", nil, nil)
	if err != nil {
		panic(err)

	}

	ctx := context.Background()

	list, err := cli.NodeList(ctx, types.NodeListOptions{})
	if err != nil {
		log.Fatalf("docker api returned an error: %s\n", err.Error())
	}
	nodes := []string{}

	for _, node := range list {
		if node.Spec.Role == "manager" {
			if avoidMasters == 0 {
				nodes = append(nodes, node.Description.Hostname)
			}
		} else {
			nodes = append(nodes, node.Description.Hostname)
		}

	}
	return nodes
}

func getServiceDefinition(cli *client.Client, replicas uint64, network string) swarm.ServiceSpec {
	// container specs
	container := swarm.ContainerSpec{Image: "nicgrobler/pinger:v3.0.0"}
	// task specs - replica count
	reps := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}}
	// network to attach to
	nets := swarm.NetworkAttachmentConfig{Target: network}
	serviceSpec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{ContainerSpec: container, Networks: []swarm.NetworkAttachmentConfig{nets}}, Mode: reps}

	return serviceSpec

}

func main() {

	// get config
	c, err := getConfig()
	if err != nil {
		log.Fatalf("startup failed due to a config error: %s", err.Error())
	}

	// get network list
	networks := getNetworkList(c.AvoidNetworks)
	if len(networks) == 0 {
		log.Fatalln("no overlay networks found")
	}

	// get network list
	nodes := getNodeList(c.AvoidMasters)
	if len(nodes) == 0 {
		log.Fatalln("no useable nodes found")
	}

	// build the workslist
	worklist := []swarm.ServiceSpec{}
	for _, network := range networks {
		cli, err := client.NewClient("unix:///var/run/docker.sock", "", nil, nil)
		if err != nil {
			panic(err)

		}

		s := getServiceDefinition(cli, 3, network)
		worklist = append(worklist, s)

	}

}

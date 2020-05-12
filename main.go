package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/swarm"
	"github.com/docker/docker/client"
)

type kv struct {
	key   string
	value string
}

type env map[string]kv

type envs map[string]env

type config struct {
	AvoidNetworks map[string]string
	AvoidMasters  int
}

func getKeyValue(data string) (string, string) {
	bits := strings.Split(data, "=")
	if len(bits) < 2 {
		// could be a flag (i.e. a key, with no value)
		return bits[0], ""
	}
	return bits[0], bits[1]
}

func getcontainerEnv() env {
	file, err := os.Open(".env")
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close()

	kvs := make(map[string]kv)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		l := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(l, "#") {
			// not a comment line
			k, v := getKeyValue(l)
			kvs[k] = kv{key: k, value: v}
		}
	}

	return kvs
}

func getConfig(containerEnv env) (config, error) {
	cconfig := config{}
	avoidStrings := containerEnv["AVOID_NETWORKS"]
	if avoidStrings.value != "" {
		nets := getSubStringsMap(avoidStrings.value)
		if len(nets) == 0 {
			return cconfig, errors.New("invalid value passed for AVOID_NETWORKS")
		}
		cconfig.AvoidNetworks = nets
	} else {
		// not specified, so set to default
		cconfig.AvoidNetworks = map[string]string{"ingress": "ingress"}
	}

	avoidMastersString := containerEnv["AVOID_MASTERS"]
	if avoidMastersString.value != "" {
		s, err := strconv.Atoi(avoidMastersString.value)
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

func setAndGetContainerEnv(containerEnv envs, network string) env {
	/*
		main helper that takes the supplied .env file, as would be used by a single stack
		and transforms it to use the following logic:

		1. NEW stackname becomes => stackname + - + network
		2. NEW entry, "service spec name" is created by => NEW stackname + _ + servicename
		3. service name stays the same

		this allows us to scale the same service to multiple networks
	*/

	// create new values
	stackValue := containerEnv[network]["STACK_NAME"].value
	serviceValue := containerEnv[network]["SERVICE_NAME"].value

	stackKey := containerEnv[network]["STACK_NAME"].key

	newStackName := stackValue + "_" + network
	newServiceSpecName := newStackName + "_" + serviceValue
	// replace old stackname entry
	containerEnv[network]["STACK_NAME"] = kv{key: stackKey, value: newStackName}
	// create new service spec name
	containerEnv[network]["SERVICE_SPEC_NAME"] = kv{key: "SERVICE_SPEC_NAME", value: newServiceSpecName}

	cv := containerEnv[network]
	return cv

}

func (containerEnv env) getContainerEnv() []string {
	var newContainerEnv []string
	for _, v := range containerEnv {
		newContainerEnv = append(newContainerEnv, v.key+"="+v.value)
	}

	return newContainerEnv
}

func (containerEnv env) getServiceName() string {
	return containerEnv["SERVICE_NAME"].value
}

func (containerEnv env) getServiceSpecName() string {
	return containerEnv["SERVICE_SPEC_NAME"].value
}

func (containerEnv env) getStackName() string {
	return containerEnv["STACK_NAME"].value
}

func (containerEnv env) getImage() string {
	return containerEnv["IMAGE"].value
}

func getServiceDefinition(cli *client.Client, replicas uint64, network string, cfg envs) swarm.ServiceSpec {
	e := setAndGetContainerEnv(cfg, network)
	// container specs
	container := swarm.ContainerSpec{Image: e.getImage(), Command: []string{"/go/bin/pinger"}, Env: e.getContainerEnv()}
	// task specs - replica count
	reps := swarm.ServiceMode{Replicated: &swarm.ReplicatedService{Replicas: &replicas}}
	// network to attach to
	nets := swarm.NetworkAttachmentConfig{Target: network, Aliases: []string{e.getServiceName()}}
	serviceSpec := swarm.ServiceSpec{TaskTemplate: swarm.TaskSpec{ContainerSpec: container, Networks: []swarm.NetworkAttachmentConfig{nets}}, Mode: reps}
	serviceSpec.Name = e.getServiceSpecName()
	serviceSpec.Labels = map[string]string{
		"com.docker.stack.image":     e.getImage(),
		"com.docker.stack.namespace": e.getStackName(),
	}

	return serviceSpec

}

func main() {

	// get client environment
	containerEnv := getcontainerEnv()
	// get config
	c, err := getConfig(containerEnv)
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
	cli, err := client.NewClient("unix:///var/run/docker.sock", "", nil, nil)
	if err != nil {
		panic(err)

	}

	/*
		We need to modify the basic container environment for each stack. here we create a structure for handling this
	*/
	configs := make(map[string]env)
	for _, network := range networks {
		configs[network] = containerEnv
	}

	/*
		Create the service config specific for this network
	*/
	for _, network := range networks {
		numberOfNodes := len(nodes)
		s := getServiceDefinition(cli, uint64(numberOfNodes), network, configs)
		worklist = append(worklist, s)
	}

	ctx := context.Background()
	// execute worklist sequentially
	for _, work := range worklist {
		_, err := cli.ServiceCreate(ctx, work, types.ServiceCreateOptions{})
		if err != nil {
			log.Fatalf("unable to create service: %s\n", err.Error())
		}
		fmt.Printf("created server: %s\n", work.Name)
	}

}

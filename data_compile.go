package main

import (
	"archive/zip"
	"bufio"
	"encoding/hex"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/hashicorp/terraform/helper/schema"
	"golang.org/x/net/context"
)

func dataCompile() *schema.Resource {
	return &schema.Resource{
		Read: resourceCompileRead,

		Schema: map[string]*schema.Schema{
			"filename": {
				Type:     schema.TypeString,
				Required: true,
			},
			"input": {
				Type:     schema.TypeString,
				Required: true,
			},
			"output": {
				Type:     schema.TypeString,
				Required: true,
			},
			"image": {
				Type:     schema.TypeString,
				Required: true,
			},
			"script": {
				Type:     schema.TypeString,
				Required: true,
			},
		},
	}
}

func resourceCompileRead(d *schema.ResourceData, meta interface{}) error {
	log.Printf("[DEBUG] --------------- Read\n")

	cli := meta.(*client.Client)

	imageStr := d.Get("image").(string)
	filenameStr := d.Get("filename").(string)
	inputStr := d.Get("input").(string)
	outputStr := d.Get("output").(string)
	scriptStr := d.Get("script").(string)

	// Check input dir exist
	if _, err := dirExist(inputStr); err != nil {
		return err
	}

	// Check output dir exist
	err := os.MkdirAll(outputStr, 0755)
	if err != nil {
		return err
	}

	// Check script file exist
	scriptFilePath := path.Join(inputStr, scriptStr)
	if err := fileExist(scriptFilePath); err != nil {
		return err
	}

	// Generate md5 input files
	md5sInput, err := getDirFilesMD5(inputStr, "listing")
	if err != nil {
		return err
	}

	// Check output file exist
	outputFilePath := path.Join(outputStr, filenameStr)
	if err := fileExist(outputFilePath); err == nil {
		log.Printf("[DEBUG] output file exist %s, check md5 listing", outputFilePath)

		md5sArchive, err := getArchiveListing(outputFilePath, "listing")
		if err != nil {
			return err
		}

		// Check if files has changed
		equal := reflect.DeepEqual(md5sInput, md5sArchive)
		if equal {
			log.Println("[DEBUG] equal, no need to continue")
			return nil
		} else {
			log.Println("[DEBUG] not equal, recompile")
		}
	}

	// Save listing file
	if err := generateListingFile(md5sInput, inputStr, "listing"); err != nil {
		return err
	}

	// Compile code
	if err = compileWithContainer(cli, imageStr, filenameStr, inputStr, outputStr, scriptStr); err != nil {
		return err
	}

	// Delete listing file
	if err = deleteListingFile(inputStr, "listing"); err != nil {
		return err
	}

	return nil
}

func dirExist(path string) (bool, error) {
	pathInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}

	if pathInfo.Mode().IsRegular() {
		return false, errors.New("folder is a file")
	}

	if !os.IsNotExist(err) {
		return false, nil
	}

	return true, nil
}

func deleteListingFile(folder string, file string) error {
	filePath := path.Join(folder, file)
	return os.Remove(filePath)
}

func generateListingFile(md5sInput map[string]string, folder string, file string) error {
	// Save to file
	listingFile := path.Join(folder, file)
	f, err := os.OpenFile(listingFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		panic(err)
	}
	defer f.Close()

	for k, v := range md5sInput {
		if k == fmt.Sprintf("/%s", file) {
			continue
		}
		message := fmt.Sprintf("%s %s\n", k, v)
		if _, err = f.WriteString(message); err != nil {
			return err
		}
	}

	return nil
}

func getArchiveListing(archivePath string, listingFilename string) (map[string]string, error) {

	read, err := zip.OpenReader(archivePath)

	// TODO if file corrupt regenerate
	if err != nil {
		message := fmt.Sprintf("Failed to open: %s", archivePath)
		return nil, errors.New(message)
	}
	defer read.Close()

	var zipMD5 = map[string]string{}
	for _, file := range read.File {
		if file.Name == listingFilename {

			// Get content
			reader, err := file.Open()
			if err != nil {
				msg := "[DEBUG] Failed to open zip %s for reading: %s"
				return nil, fmt.Errorf(msg, file.Name, err)
			}

			// Read line by line
			scanner := bufio.NewScanner(reader)
			for scanner.Scan() {
				split := strings.Split(scanner.Text(), " ")
				zipMD5[split[0]] = split[1]
			}

			if err := scanner.Err(); err != nil {
				return nil, err
			}

			if err = reader.Close(); err != nil {
				return nil, err
			}

			return zipMD5, nil
		}
	}

	return nil, errors.New("unable to find listing file")
}

func getDirFilesMD5(path string, listing string) (map[string]string, error) {
	// Get md5s
	md5s, err := MD5All(path)
	if err != nil {
		return nil, err
	}

	// Sort
	var paths []string
	for p := range md5s {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	// re-create map
	var md5map = map[string]string{}
	for _, p := range paths {
		if p == fmt.Sprintf("/%s", listing) {
			continue
		}
		md5hex := md5s[p]
		md5map[p] = hex.EncodeToString(md5hex[:])
	}

	return md5map, nil
}

func fileExist(path string) error {
	scriptStat, err := os.Stat(path)
	if err != nil {
		return err
	}

	if !scriptStat.Mode().IsRegular() {
		return errors.New("script file does not exist")
	}

	return nil
}

func compileWithContainer(cli *client.Client, image string, filename string, input string, output string, script string) error {

	log.Printf("[DEBUG] compilation start, image: %s, input: %s, output: %s, script: %s, filename: %s\n", image, input, output, script, filename)

	ctx := context.Background()

	log.Printf("[DEBUG] download image %s\n", image)
	reader, err := cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return err
	}

	// Read data
	data, err := ioutil.ReadAll(reader)
	if err != nil {
		return err
	}
	log.Print(string(data))

	log.Println("[DEBUG] create container configuration")
	scriptPath := fmt.Sprintf("/input/%s", script)
	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: image,
		Cmd:   []string{"/bin/sh", scriptPath},
	}, &container.HostConfig{
		Mounts: []mount.Mount{
			// input folder
			{
				Type:   mount.TypeBind,
				Source: input,
				Target: "/input",
			},
			// output folder
			{
				Type:   mount.TypeBind,
				Source: output,
				Target: "/output",
			},
		},
	}, nil, "")

	if err != nil {
		return err
	}

	log.Println("[DEBUG] start container")
	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	log.Println("[DEBUG] wait for container to finish")
	_, err = cli.ContainerWait(ctx, resp.ID)
	if err != nil {
		return err
	}

	log.Println("[DEBUG] retrieving container logs")
	reader, err = cli.ContainerLogs(ctx, resp.ID, types.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return err
	}

	// Read data
	data, err = ioutil.ReadAll(reader)
	if err != nil {
		return err
	}
	log.Print(string(data))

	return nil
}

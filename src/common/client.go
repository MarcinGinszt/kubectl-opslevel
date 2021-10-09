package common

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/opslevel/opslevel-go"
	"github.com/rs/zerolog/log"
)

func ReconcileService(client *opslevel.Client, service *ServiceRegistration) {
	if service == nil {
		return
	}
	s := *service
	if len(s.Aliases) <= 0 {
		log.Warn().Msgf("[%s] found 0 aliases from kubernetes data", s.Name)
		return
	}
	log.Trace().Msgf("[%s] Parsed Data: \n%s", s.Name, s.toPrettyJson())
	foundService, needsUpdate := findService(client, s)
	if foundService == nil {
		if s.Name == "" {
			log.Warn().Msgf("unable to create service with an empty name.  aliases = [\"%s\"]", strings.Join(s.Aliases, "\", \""))
			return
		}
		newService, newServiceErr := createService(client, s)
		if newServiceErr != nil {
			log.Error().Msgf("[%s] Failed creating service\n\tREASON: %v", s.Name, newServiceErr.Error())
			return
		} else {
			log.Info().Msgf("[%s] Created new service", newService.Name)
		}
		foundService = newService
	}
	if needsUpdate {
		updateService(client, s, foundService)
	}
	go handleAliases(client, s, foundService)
	go handleTags(client, s, foundService)
	go handleTools(client, s, foundService)
	go handleRepositories(client, s, foundService)
	log.Info().Msgf("[%s] Finished processing data", foundService.Name)
}

func findService(client *opslevel.Client, registration ServiceRegistration) (*opslevel.Service, bool) {
	for _, alias := range registration.Aliases {
		foundService, err := client.GetServiceWithAlias(alias)
		if err == nil && foundService.Id != nil {
			log.Info().Msgf("[%s] Reconciling service found with alias '%s' ...", foundService.Name, alias)
			return foundService, true
		}
	}
	// TODO: last ditch effort - search for service with alias == registration.Name ?
	return nil, false
}

func createService(client *opslevel.Client, registration ServiceRegistration) (*opslevel.Service, error) {
	serviceCreateInput := opslevel.ServiceCreateInput{
		Name:        registration.Name,
		Product:     registration.Product,
		Description: registration.Description,
		Language:    registration.Language,
		Framework:   registration.Framework,
	}
	cache := GetOrCreateAliasCache()
	if v, ok := cache.TryGetTier(registration.Tier); ok {
		serviceCreateInput.Tier = string(v.Alias)
	}
	if v, ok := cache.TryGetLifecycle(registration.Lifecycle); ok {
		serviceCreateInput.Lifecycle = string(v.Alias)
	}
	if v, ok := cache.TryGetTeam(registration.Owner); ok {
		serviceCreateInput.Owner = string(v.Alias)
	}
	return client.CreateService(serviceCreateInput)
}

func updateService(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	updateServiceInput := opslevel.ServiceUpdateInput{
		Id:          service.Id,
		Product:     registration.Product,
		Description: registration.Description,
		Language:    registration.Language,
		Framework:   registration.Framework,
	}
	cache := GetOrCreateAliasCache()
	if v, ok := cache.TryGetTier(registration.Tier); ok {
		updateServiceInput.Tier = string(v.Alias)
	}
	if v, ok := cache.TryGetLifecycle(registration.Lifecycle); ok {
		updateServiceInput.Lifecycle = string(v.Alias)
	}
	if v, ok := cache.TryGetTeam(registration.Owner); ok {
		updateServiceInput.Owner = string(v.Alias)
	}
	updatedService, updateServiceErr := client.UpdateService(updateServiceInput)
	if updateServiceErr != nil {
		log.Error().Msgf("[%s] Failed updating service\n\tREASON: %v", service.Name, updateServiceErr.Error())
	} else {
		if diff := cmp.Diff(service, updatedService); diff != "" {
			log.Info().Msgf("[%s] Updated Service - Diff:\n%s", service.Name, diff)
		}
	}
}

func handleAliases(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	for _, alias := range registration.Aliases {
		if alias == "" || service.HasAlias(alias) {
			continue
		}
		_, err := client.CreateAlias(opslevel.AliasCreateInput{
			Alias:   alias,
			OwnerId: service.Id,
		})
		if err != nil {
			log.Error().Msgf("[%s] Failed assigning alias '%s'\n\tREASON: %v", service.Name, alias, err.Error())
		} else {
			log.Info().Msgf("[%s] Assigned alias '%s'", service.Name, alias)
		}
	}
}

func handleTags(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	assignTags(client, registration, service)
	createTags(client, registration, service)
}

func assignTags(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	if registration.TagAssigns == nil {
		return
	}
	input := opslevel.TagAssignInput{
		Id:   service.Id,
		Tags: registration.TagAssigns,
	}
	_, err := client.AssignTags(input)
	jsonBytes, _ := json.Marshal(registration.TagAssigns)
	if err != nil {
		log.Error().Msgf("[%s] Failed assigning tags: %s\n\tREASON: %v", service.Name, string(jsonBytes), err.Error())
	} else {
		log.Info().Msgf("[%s] Assigned tags: %s", service.Name, string(jsonBytes))
	}
}

func createTags(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	for _, tag := range registration.TagCreates {
		if service.HasTag(tag.Key, tag.Value) {
			continue
		}
		input := opslevel.TagCreateInput{
			Id:    service.Id,
			Key:   tag.Key,
			Value: tag.Value,
		}
		_, err := client.CreateTag(input)
		if err != nil {
			log.Error().Msgf("[%s] Failed creating tag '%s = %s'\n\tREASON: %v", service.Name, tag.Key, tag.Value, err.Error())
		} else {
			log.Info().Msgf("[%s] Created tag '%s = %s'", service.Name, tag.Key, tag.Value)
		}
	}
}

func handleTools(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	for _, tool := range registration.Tools {
		if service.HasTool(tool.Category, tool.DisplayName, tool.Environment) {
			log.Debug().Msgf("[%s] Tool '{Category: %s, Environment: %s, Name: %s}' already exists on service ... skipping", service.Name, tool.Category, tool.Environment, tool.DisplayName)
			continue
		}
		tool.ServiceId = service.Id
		_, err := client.CreateTool(tool)
		if err != nil {
			log.Error().Msgf("[%s] Failed assigning tool '{Category: %s, Environment: %s, Name: %s}'\n\tREASON: %v", service.Name, tool.Category, tool.Environment, tool.DisplayName, err.Error())
		} else {
			log.Info().Msgf("[%s] Ensured tool '{Category: %s, Environment: %s, Name: %s}'", service.Name, tool.Category, tool.Environment, tool.DisplayName)
		}
	}
}

func handleRepositories(client *opslevel.Client, registration ServiceRegistration, service *opslevel.Service) {
	for _, repositoryCreate := range registration.Repositories {
		repositoryAsString := fmt.Sprintf("{Alias: %s, Directory: %s, Name: %s}", repositoryCreate.Repository.Alias, repositoryCreate.BaseDirectory, repositoryCreate.DisplayName)
		foundRepository, foundRepositoryErr := client.GetRepositoryWithAlias(string(repositoryCreate.Repository.Alias))
		if foundRepositoryErr != nil {
			log.Warn().Msgf("[%s] Repository with alias: '%s' not found so it cannot be attached to service ... skipping", service.Name, repositoryAsString)
			continue
		}
		serviceRepository := foundRepository.GetService(service.Id, repositoryCreate.BaseDirectory)
		if serviceRepository != nil {
			if repositoryCreate.DisplayName != "" && serviceRepository.DisplayName != repositoryCreate.DisplayName {
				repositoryUpdate := opslevel.ServiceRepositoryUpdateInput{
					Id:          serviceRepository.Id,
					DisplayName: repositoryCreate.DisplayName,
				}
				_, err := client.UpdateServiceRepository(repositoryUpdate)
				if err != nil {
					log.Error().Msgf("[%s] Failed updating repository '%s'\n\tREASON: %v", service.Name, repositoryAsString, err.Error())
					continue
				} else {
					log.Info().Msgf("[%s] Updated repository '%s'", service.Name, repositoryAsString)
					continue
				}
			}
			log.Debug().Msgf("[%s] Repository '%s' already attached to service ... skipping", service.Name, repositoryAsString)
			continue
		}
		repositoryCreate.Service = opslevel.IdentifierInput{Id: service.Id}
		_, err := client.CreateServiceRepository(repositoryCreate)
		if err != nil {
			log.Error().Msgf("[%s] Failed assigning repository '$s'\n\tREASON: %v", service.Name, repositoryAsString, err.Error())
		} else {
			log.Info().Msgf("[%s] Attached repository '%s'", service.Name, repositoryAsString)
		}
	}
}

package memory

import (
	"context"
	"encoding/json"
	"fmt"

	"flux/pkg/models"

	"github.com/redis/go-redis/v9"
)

type RedisMemory struct {
	client *redis.Client
	ctx    context.Context
}

func NewRedisMemory(addr string) *RedisMemory {
	client := redis.NewClient(&redis.Options{
		Addr: addr,
	})

	return &RedisMemory{
		client: client,
		ctx:    context.Background(),
	}
}

func (r *RedisMemory) Close() error {
	return r.client.Close()
}

// Function operations
func (r *RedisMemory) SaveFunction(function *models.Function) error {
	data, err := json.Marshal(function)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("function:%s", function.Name)
	return r.client.Set(r.ctx, key, data, 0).Err()
}

func (r *RedisMemory) GetFunction(name string) (*models.Function, error) {
	key := fmt.Sprintf("function:%s", name)
	data, err := r.client.Get(r.ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var function models.Function
	if err := json.Unmarshal(data, &function); err != nil {
		return nil, err
	}

	return &function, nil
}

func (r *RedisMemory) GetAllFunctions() ([]*models.Function, error) {
	keys, err := r.client.Keys(r.ctx, "function:*").Result()
	if err != nil {
		return nil, err
	}

	functions := make([]*models.Function, 0, len(keys))
	for _, key := range keys {
		data, err := r.client.Get(r.ctx, key).Bytes()
		if err != nil {
			continue
		}

		var function models.Function
		if err := json.Unmarshal(data, &function); err != nil {
			continue
		}

		functions = append(functions, &function)
	}

	return functions, nil
}

// Agent operations
func (r *RedisMemory) SaveAgent(agent *models.Agent) error {
	data, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("agent:%s", agent.ID)
	return r.client.Set(r.ctx, key, data, 0).Err()
}

func (r *RedisMemory) GetAgent(id string) (*models.Agent, error) {
	key := fmt.Sprintf("agent:%s", id)
	data, err := r.client.Get(r.ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	var agent models.Agent
	if err := json.Unmarshal(data, &agent); err != nil {
		return nil, err
	}

	return &agent, nil
}

func (r *RedisMemory) GetAllAgents() ([]*models.Agent, error) {
	keys, err := r.client.Keys(r.ctx, "agent:*").Result()
	if err != nil {
		return nil, err
	}

	agents := make([]*models.Agent, 0, len(keys))
	for _, key := range keys {
		data, err := r.client.Get(r.ctx, key).Bytes()
		if err != nil {
			continue
		}

		var agent models.Agent
		if err := json.Unmarshal(data, &agent); err != nil {
			continue
		}

		agents = append(agents, &agent)
	}

	return agents, nil
}

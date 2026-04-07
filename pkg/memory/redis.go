package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"flux/pkg/config"
	"flux/pkg/models"

	"github.com/redis/go-redis/v9"
)

type RedisMemory struct {
	client *redis.Client
	ctx    context.Context
}

func NewRedisMemory() *RedisMemory {
	addr := config.Get().RedisAddr
	opt, err := redis.ParseURL(addr)
	if err != nil {
		panic(err)
	}

	log.Printf("[memory] Connected to Redis at %s", addr)
	return &RedisMemory{
		client: redis.NewClient(opt),
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
	if err != nil || len(keys) == 0 {
		return nil, err
	}

	values, err := r.client.MGet(r.ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	functions := make([]*models.Function, 0, len(values))
	for _, v := range values {
		if v == nil {
			continue
		}
		var function models.Function
		if err := json.Unmarshal([]byte(v.(string)), &function); err != nil {
			continue
		}
		functions = append(functions, &function)
	}

	return functions, nil
}

// Code archive operations
func (r *RedisMemory) SaveCodeArchive(functionName string, data []byte) error {
	key := fmt.Sprintf("code:%s", functionName)
	return r.client.Set(r.ctx, key, data, 0).Err()
}

func (r *RedisMemory) GetCodeArchive(functionName string) ([]byte, error) {
	key := fmt.Sprintf("code:%s", functionName)
	data, err := r.client.Get(r.ctx, key).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	return data, nil
}

// Agent operations
func (r *RedisMemory) SaveAgent(agent *models.Agent) error {
	data, err := json.Marshal(agent)
	if err != nil {
		return err
	}

	key := fmt.Sprintf("flux:agents:%s", agent.ID)
	return r.client.Set(r.ctx, key, data, 0).Err()
}

func (r *RedisMemory) GetAgent(id string) (*models.Agent, error) {
	key := fmt.Sprintf("flux:agents:%s", id)
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

func (r *RedisMemory) DeleteAgent(id string) error {
	return r.client.Del(r.ctx, fmt.Sprintf("flux:agents:%s", id)).Err()
}

func (r *RedisMemory) GetExecution(executionID string) (*models.ExecutionRecord, error) {
	data, err := r.client.Get(r.ctx, fmt.Sprintf("flux:exec:%s", executionID)).Bytes()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}
	var record models.ExecutionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *RedisMemory) GetAllAgents() ([]*models.Agent, error) {
	keys, err := r.client.Keys(r.ctx, "flux:agents:*").Result()
	if err != nil || len(keys) == 0 {
		return nil, err
	}

	values, err := r.client.MGet(r.ctx, keys...).Result()
	if err != nil {
		return nil, err
	}

	agents := make([]*models.Agent, 0, len(values))
	for _, v := range values {
		if v == nil {
			continue
		}
		var agent models.Agent
		if err := json.Unmarshal([]byte(v.(string)), &agent); err != nil {
			continue
		}
		agents = append(agents, &agent)
	}

	return agents, nil
}

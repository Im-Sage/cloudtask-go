package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Im-Sage/cloudtask-go/day9/internal/cache"
	"github.com/Im-Sage/cloudtask-go/day9/internal/model"
	"github.com/Im-Sage/cloudtask-go/day9/internal/repository"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// 业务规则、事务用例

type TaskService struct {
	taskRepository *repository.TaskRepository
	taskCache      *cache.TaskCache
	taskBloom      *cache.TaskBloom
	loadGroup      singleflight.Group
	// 这里使用信号量限制同时执行的数量,必须作为共享变量，要不然每个请求都将构建一个管道
	slots   chan struct{}
	limiter *rate.Limiter
}

func NewTaskService(
	taskRepository *repository.TaskRepository,
	taskCache *cache.TaskCache,
	taskBloom *cache.TaskBloom,
) *TaskService {
	return &TaskService{
		taskRepository: taskRepository,
		taskCache:      taskCache,
		taskBloom:      taskBloom,
		slots:          make(chan struct{}, 10),
		limiter:        rate.NewLimiter(rate.Limit(10), 5),
	}
}

var ErrInvalidTitle = errors.New("invalid title")
var ErrInvalidDescription = errors.New("invalid description")
var ErrInvalidStatus = errors.New("invalid status (should be 'pending' or 'in_progress' or 'completed')")

func (s *TaskService) CreateTask(
	ctx context.Context,
	title string,
	description string,
) (model.Task, error) {
	if strings.TrimSpace(title) == "" {
		return model.Task{}, ErrInvalidTitle
	}
	task, err := s.taskRepository.CreateTask(ctx, title, description)
	if err != nil {
		return model.Task{}, err
	}
	if s.taskBloom != nil {
		// 插入成功时把新建任务加入过滤器
		if err := s.taskBloom.Add(ctx, task.ID); err != nil {
			log.Printf("任务创建成功但加入布隆过滤器失败：id=%d err=%v",
				task.ID, err)
		}
	}

	return task, nil
}

func (s *TaskService) GetTaskByID(
	ctx context.Context,
	id int64,
) (model.Task, error) {
	// 布隆过滤器不一定可用
	if s.taskBloom == nil {
		log.Printf("布隆过滤器不可用")
	} else { // 布隆出错或者id可能存在，要查询缓存或者是数据库
		// 首先使用布隆过滤器判断id是否一定不存在
		exists, err := s.taskBloom.MayContain(ctx, id)
		if exists == false && err == nil {
			// id必不存在，直接返回
			log.Printf("使用了布隆过滤器")
			return model.Task{}, repository.ErrTaskNotFound
		}
		if err != nil {
			log.Printf("布隆过滤器查询失败，继续查询缓存：%v", err)
		}
	}
	// 如果启用了缓存，调用TaskCache.Get(ctx, id)，首先得明白什么叫启用缓存
	cacheCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if s.taskCache != nil {
		log.Printf("启用了缓存")
		cacheResult, err := s.taskCache.GetResult(cacheCtx, id)
		if err != nil {
			// redis连接对象存在，但本次缓存操作失败
			log.Printf("连接对象存在，但本次操作失败,原因：%v, 准备回源", err)
		} else if cacheResult.Hit {
			// 空值缓存时，exits为false，若不对exits作判断，则当成查询成功，会返回200和空任务
			if !cacheResult.Exists {
				return model.Task{}, repository.ErrTaskNotFound
			}
			// exits为true则说明数据库中本身就有该task
			return cacheResult.Task, nil
		}
	}
	// 未命中则准备开始回源
	// 使用singleflight解决缓存击穿问题，singleflight的顺序是先检查缓存再查数据库
	key := fmt.Sprintf("task:%d", id)
	value, err, _ := s.loadGroup.Do(key,
		func() (interface{}, error) {
			// 再次查询缓存
			if s.taskCache != nil {
				checkCtx, checkCancel := context.WithTimeout(ctx, 100*time.Millisecond)
				defer checkCancel()
				cacheResult, err := s.taskCache.GetResult(checkCtx, id)
				if err != nil {
					// redis连接对象存在，但本次缓存操作失败
					log.Printf("连接对象存在，但本次操作失败,原因：%v, 准备回源", err)
				} else if cacheResult.Hit {
					// 空值缓存时，exits为false，若不对exits作判断，则当成查询成功，会返回200和空任务
					if !cacheResult.Exists {
						return model.Task{}, repository.ErrTaskNotFound
					}
					// exits为true则说明数据库中本身就有该task
					return cacheResult.Task, nil
				}
			}
			// 缓存依然没有命中，查询数据库

			// 使用令牌桶控制每秒的请求速率
			if !s.limiter.Allow(){
				return nil, repository.ErrRateLimited
			}

			// 使用信号量控制同时执行的请求数量
			select {
			case s.slots <- struct{}{}: // 获取名额
				defer func() { <-s.slots }()
				if err := ctx.Err(); err != nil { // 防止有空位和context超时 同时就绪的情况
					return nil, err
				}
			case <-ctx.Done():
				return nil, ctx.Err()
			}

			task, err := s.taskRepository.GetTaskByID(ctx, id)
			// 这里task可能不存在，如果多次访问不存在task这就是缓存穿透
			// 解决方法：既然PostgreSQL确认某个任务不存在，就可以把任务不存这个结果也缓存起来
			// 另外一个方案是布隆过滤器
			if errors.Is(err, repository.ErrTaskNotFound) {
				// 判断是否启用了缓存
				if s.taskCache != nil {
					// 数据库中task不存在，在cache中缓存一个空值
					if err := s.taskCache.SetNotFound(ctx, id); err != nil {
						log.Printf("空值写入缓存失败：%v", err)
						// return model.Task{}, err  redis宕机时，空值会写入失败，本质上还是任务不存在，
					}
				}
				// / 无论缓存是否写入成功，都保留数据库的“任务不存在”结果。
				return model.Task{}, err
			}
			if err != nil {
				return model.Task{}, err
			}
			// 数据库查询成功后回填
			if s.taskCache != nil {
				// 将task写进Redis, Redis写入失败不应该影响数据库查询结果
				if err := s.taskCache.SetEntry(ctx, id, task); err != nil {
					log.Printf("写入redis失败：%v", err)
				}
			}
			return task, nil
		})
	if err != nil {
		return model.Task{}, err
	}
	task, ok := value.(model.Task)
	if !ok {
		return model.Task{}, fmt.Errorf(
			"singleflight 返回类型异常：%T", value,
		)
	}
	return task, nil
}

func (s *TaskService) GetAllTasks(
	ctx context.Context,
	limit int,
	offset int,
	status string) ([]model.Task, error) {
	return s.taskRepository.GetAllTasks(ctx, limit, offset, status)
}

func (s *TaskService) DeleteTask(ctx context.Context, id int64) error {
	return s.taskRepository.DeleteTask(ctx, id)
}

func (s *TaskService) UpdateTask(
	ctx context.Context,
	id int64,
	title string,
	status string) (model.Task, error) {
	if status != "pending" && status != "in_progress" && status != "completed" {
		return model.Task{}, ErrInvalidStatus
	}
	if strings.TrimSpace(title) == "" {
		return model.Task{}, ErrInvalidTitle
	}
	// 先写数据库，成功后删除缓存，让下一次读取重新从数据库加载。
	task, err := s.taskRepository.UpdateTask(ctx, id, title, status)
	// 数据库更新失败直接返回错误，不删除缓存
	if err != nil {
		return model.Task{}, err
	}
	// 数据库更新成功则删除当前缓存，首先要判断是否开启了缓存
	if s.taskCache != nil {
		if err := s.taskCache.DeleteTask(ctx, id); err != nil {
			// 若数据库删除失败不影响数据库更新操作
			log.Printf("删除缓存失败：%v", err)
		}
	}
	return task, nil
}

func (s *TaskService) ChangeStatus(ctx context.Context, id int64, newStatus string) error {
	if newStatus != "pending" && newStatus != "in_progress" && newStatus != "completed" {
		return ErrInvalidStatus
	}
	return s.taskRepository.ChangeStatus(ctx, id, newStatus)
}

func (s *TaskService) ChangeStatusOptimistis(ctx context.Context, id int64, newStatus string, expectedVersion int64) (model.Task, error) {
	if newStatus != "pending" && newStatus != "in_progress" && newStatus != "completed" {
		return model.Task{}, ErrInvalidStatus
	}
	return s.taskRepository.ChangeStatusOptimistis(ctx, id, newStatus, expectedVersion)
}

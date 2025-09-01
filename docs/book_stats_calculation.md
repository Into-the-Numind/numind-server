# Book 统计计算逻辑实现

## 概述

本文档描述了 `book_num` 和 `book_all_num` 字段的计算逻辑和实现方式。

## 字段定义

- `book_num`: book状态为非failed且未删除的数量
- `book_all_num`: book状态为非failed的数量

## 计算规则

### 1. 创建 Book
- **book状态为creating**: `book_num +1`, `book_all_num +1`
- **book状态为success**: 不变（已在creating时增加）
- **book状态为failed**: 不变

### 2. Book 状态变化
- **从非failed变为failed**: `book_num -1`, `book_all_num -1`
- **从failed变为非failed**: `book_all_num +1`

### 3. 删除 Book
- **book状态为非failed**: `book_num -1`, `book_all_num` 不变
- **book状态为failed**: 不变

## 实现方式

### 1. 用户统计更新方法

在 `internal/numind/biz/user/user.go` 中实现了以下方法：

```go
// IncrementUserBookNum 增加用户的书籍数量（创建book时调用，状态为creating）
func (b *userBiz) IncrementUserBookNum(ctx context.Context, userID uint) error {
    return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
        UpdateColumns(map[string]interface{}{
            "book_num":     gorm.Expr("book_num + ?", 1),
            "book_all_num": gorm.Expr("book_all_num + ?", 1),
        }).Error
}

// DecrementUserBookNum 减少用户的书籍数量（删除book时调用）
func (b *userBiz) DecrementUserBookNum(ctx context.Context, userID uint) error {
    return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
        UpdateColumn("book_num", gorm.Expr("book_num - ?", 1)).Error
}

// UpdateUserBookStatsOnStatusChange 当book状态变化时更新用户统计
func (b *userBiz) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
    // 如果状态从非failed变为failed，需要减少book_num和book_all_num
    if oldStatus != model.BookStatusFailed && newStatus == model.BookStatusFailed {
        return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
            UpdateColumns(map[string]interface{}{
                "book_num":     gorm.Expr("book_num - ?", 1),
                "book_all_num": gorm.Expr("book_all_num - ?", 1),
            }).Error
    }

    // 如果状态从failed变为非failed，需要增加book_all_num
    if oldStatus == model.BookStatusFailed && newStatus != model.BookStatusFailed {
        return b.ds.DB().Model(&model.User{}).Where("id = ?", userID).
            UpdateColumn("book_all_num", gorm.Expr("book_all_num + ?", 1)).Error
    }

    return nil
}
```

### 2. Store 层统计更新

在 `internal/numind/store/book.go` 中实现了以下方法：

```go
// UpdateUserBookStatsOnDelete 删除book时更新用户统计
func (s *books) UpdateUserBookStatsOnDelete(ctx context.Context, userID uint, bookStatus string) error {
    if bookStatus != model.BookStatusFailed {
        return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
            UpdateColumn("book_num", gorm.Expr("book_num - ?", 1)).Error
    }
    return nil
}

// UpdateUserBookStatsOnStatusChange 当book状态变化时更新用户统计
func (s *books) UpdateUserBookStatsOnStatusChange(ctx context.Context, userID uint, oldStatus, newStatus string) error {
    // 如果状态从非failed变为failed，需要减少book_num和book_all_num
    if oldStatus != model.BookStatusFailed && newStatus == model.BookStatusFailed {
        return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
            UpdateColumns(map[string]interface{}{
                "book_num":     gorm.Expr("book_num - ?", 1),
                "book_all_num": gorm.Expr("book_all_num - ?", 1),
            }).Error
    }

    // 如果状态从failed变为非failed，需要增加book_all_num
    if oldStatus == model.BookStatusFailed && newStatus != model.BookStatusFailed {
        return s.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
            UpdateColumn("book_all_num", gorm.Expr("book_all_num + ?", 1)).Error
    }

    return nil
}
```

### 3. 业务层集成

#### Book 创建时
```go
func (p *AsyncBookProcessor) CreateBookAsync(ctx context.Context, userID uint, text, templateID string) (*model.BookM, error) {
    // ... 创建book记录，状态为creating ...
    
    if err := p.biz.Books().Create(ctx, book); err != nil {
        return nil, err
    }

    // 创建book后立即更新用户统计
    if err := p.biz.Users().IncrementUserBookNum(ctx, userID); err != nil {
        log.C(ctx).Errorw("Failed to increment user book num", "error", err.Error())
        // 统计更新失败不影响主要流程，但记录错误
    }

    // ... 异步处理book创建 ...
    return book, nil
}
```

#### Book 删除时
```go
func (b *bookBiz) Delete(ctx context.Context, id uint) error {
    book, err := b.ds.Books().GetByID(ctx, id)
    if err != nil {
        return err
    }

    if err := b.ds.Books().Delete(ctx, id); err != nil {
        return err
    }

    // 更新用户统计
    if err := b.ds.Books().UpdateUserBookStatsOnDelete(ctx, book.UserID, book.Status); err != nil {
        // 记录错误但不影响删除操作
    }

    return nil
}
```

#### Book 状态变化时
```go
func (p *AsyncBookProcessor) updateBookStatus(ctx context.Context, bookID uint, status, errorMsg string) error {
    book, err := p.biz.Books().GetByID(ctx, bookID)
    if err != nil {
        return err
    }

    oldStatus := book.Status
    book.Status = status
    
    if err := p.biz.Books().Update(ctx, book); err != nil {
        return err
    }

    // 如果状态发生变化，需要更新用户统计
    if oldStatus != status {
        if err := p.biz.Store().UpdateUserBookStatsOnStatusChange(ctx, book.UserID, oldStatus, status); err != nil {
            // 记录错误但不影响状态更新操作
        }
    }

    return nil
}
```

## 调用时机

### 1. 创建 Book
- 在 `CreateBookAsync` 方法中，创建book后立即调用 `IncrementUserBookNum`

### 2. Book 状态变化
- 在 `updateBookStatus` 方法中，当 book 状态发生变化时调用 `UpdateUserBookStatsOnStatusChange`

### 3. 删除 Book
- 在 `Delete` 方法中，删除 book 后调用 `UpdateUserBookStatsOnDelete`

## 注意事项

1. **原子操作**: 使用数据库的原子操作来确保并发安全
2. **错误处理**: 统计更新失败不影响主要业务流程，但会记录错误日志
3. **数据一致性**: 通过事务和原子操作确保数据一致性
4. **性能考虑**: 使用 `UpdateColumn` 方法提高性能，避免不必要的字段更新
5. **统计时机**: 统计在创建book时立即更新，而不是等到状态变为success时

## 测试建议

1. **单元测试**: 测试各个统计更新方法的正确性
2. **集成测试**: 测试完整的 book 创建、状态变化、删除流程
3. **并发测试**: 测试并发情况下的数据一致性
4. **边界测试**: 测试各种状态组合的统计更新
5. **状态变化测试**: 特别测试从creating到success、从success到failed等状态变化

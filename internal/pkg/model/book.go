package model

type BookM struct {
}

func (BookM) TableName() string {
	return "book"
}

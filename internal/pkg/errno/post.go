package errno

// ErrPostNotFound 表示未找到博客.
var ErrPostNotFound = &Errno{HTTP: 404, Code: "ResourceNotFound.PostNotFound", Message: "Post was not found."}

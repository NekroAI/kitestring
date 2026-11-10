// Package fsperm 把文件收紧成只有本人能读写（存凭据、主密钥、设备 token 的文件）。
// macOS / Linux 是权限 0600；Windows 的权限位不起作用，改为设置只含当前用户的访问控制列表（不继承上级目录）。
package fsperm

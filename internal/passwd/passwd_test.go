package passwd

import "testing"

func TestHashVerify(t *testing.T) {
	hash, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !Verify(hash, "correct horse battery staple") {
		t.Fatal("正确密码应通过")
	}
	if Verify(hash, "wrong password") {
		t.Fatal("错误密码不应通过")
	}
	if Verify("garbage", "whatever") {
		t.Fatal("非法哈希不应通过")
	}
	other, err := Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if hash == other {
		t.Fatal("相同密码应产生不同盐的哈希")
	}
	if _, err := Hash("short"); err == nil {
		t.Fatal("过短密码应报错")
	}
}

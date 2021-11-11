package tool

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"
)

func AES128Encrypt(origData, key, iv []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	if len(iv) == 0 {
		iv = key
	}
	origData = pkcs5Padding(origData, blockSize)
	blockMode := cipher.NewCBCEncrypter(block, iv[:blockSize])
	crypted := make([]byte, len(origData))
	blockMode.CryptBlocks(crypted, origData)
	return crypted, nil
}

func AES128Decrypt(crypted, key, iv []byte, url string) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	blockSize := block.BlockSize()
	if len(iv) == 0 {
		iv = key
	}
	if len(crypted)%blockSize != 0 {
		fmt.Println("len error ", len(crypted), ", url is ", url, ", retry")
		// crypted = MakeBlocksFull(crypted, blockSize)
		return nil, errors.New("input not full blocks")
	}
	blockMode := cipher.NewCBCDecrypter(block, iv[:blockSize])
	origData := make([]byte, len(crypted))
	blockMode.CryptBlocks(origData, crypted)
	origData = pkcs5UnPadding(origData)
	return origData, nil
}

func MakeBlocksFull(src []byte, blockSize int) []byte {

	//1. 获取src的长度， blockSize对于des是8
	length := len(src)

	//2. 对blockSize进行取余数， 4
	remains := length % blockSize

	//3. 获取要填的数量 = blockSize - 余数
	paddingNumber := blockSize - remains //4

	//4. 将填充的数字转换成字符， 4， '4'， 创建了只有一个字符的切片
	//s1 = []byte{'4'}
	s1 := []byte{byte(paddingNumber)}

	//5. 创造一个有4个'4'的切片
	//s2 = []byte{'4', '4', '4', '4'}
	s2 := bytes.Repeat(s1, paddingNumber)

	//6. 将填充的切片追加到src后面
	s3 := append(src, s2...)

	return s3
}
func pkcs5Padding(cipherText []byte, blockSize int) []byte {
	padding := blockSize - len(cipherText)%blockSize
	padText := bytes.Repeat([]byte{byte(padding)}, padding)
	return append(cipherText, padText...)
}

func pkcs5UnPadding(origData []byte) []byte {
	length := len(origData)
	unPadding := int(origData[length-1])
	return origData[:(length - unPadding)]
}

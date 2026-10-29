# 5 मिनट में शुरुआत

**आप क्या करेंगे:** अपने कंप्यूटर पर Tijori इंस्टॉल करेंगे, उसे चालू करेंगे, एक फ़ाइल अपलोड करेंगे और उसे एडमिन पैनल में देखेंगे।

## 1. इंस्टॉल करें

Linux या macOS पर टर्मिनल खोलें और यह चलाएँ:

```sh
curl -fsSL https://raw.githubusercontent.com/getsettalk/Tijori/main/scripts/install.sh | sh
```

Windows पर [रिलीज़ पेज](https://github.com/getsettalk/Tijori/releases) से `windows_amd64.zip` डाउनलोड करें और नीचे `tijori` की जगह `tijori.exe` लिखें।

जाँचें कि यह इंस्टॉल हो गया:

```sh
tijori version
```

## 2. सेटअप करें

```sh
mkdir my-storage && cd my-storage
tijori init
```

यह एडमिन पासवर्ड पूछेगा (Enter दबाने पर अपने-आप बन जाएगा) और आपकी पहली **एक्सेस कुंजी** और **सीक्रेट कुंजी** दिखाएगा।

**सीक्रेट कुंजी अभी कॉपी कर लें।** यह सिर्फ़ एक बार दिखती है।

## 3. चालू करें

```sh
tijori serve
```

यह टर्मिनल खुला रहने दें। दूसरे टर्मिनल में जाँचें:

```sh
curl http://localhost:9000/health
```

जवाब `{"status":"ok"}` आना चाहिए।

## 4. एडमिन पैनल

1. ब्राउज़र में <http://localhost:9000/_admin/> खोलें और `admin` से लॉग इन करें।
2. **बकेट → नया बकेट** में `my-files` नाम का बकेट बनाएँ।
3. **खोलें** पर क्लिक करें और कोई फ़ोटो पेज पर खींचकर छोड़ें।

सेटिंग्स में भाषा "हिन्दी" चुनें, तो पूरा पैनल हिन्दी में दिखेगा।

## 5. अपना ऐप जोड़ें

किसी भी S3 लाइब्रेरी में ये सेटिंग्स डालें:

| सेटिंग | मान |
| --- | --- |
| Endpoint | `http://localhost:9000` |
| Region | `auto` |
| Access key / Secret key | चरण 2 वाली कुंजियाँ |
| Path-style URLs | **चालू** |

Laravel और Node.js के उदाहरण अंग्रेज़ी गाइड में हैं: [Laravel](../laravel.md), [Node.js](../nodejs.md)।

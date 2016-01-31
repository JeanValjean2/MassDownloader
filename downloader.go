package main

import "fmt"
import "strings"
import "io/ioutil"
import "net/http"
import "net/url"
import "time"
import "os"
import "strconv"
import "github.com/vincent-petithory/dataurl"

/*
	Téléchargeur respectueux : on télécharge plusieurs ressources HTTP en parallèle mais jamais d'un même site, pour ne pas faire chauffer les serveurs inutilement et pour ne pas se faire bannir.

	L'usage typique de ce script est de télécharger un grand nombre de ressources HTTP (images, pages) et de stocker ces ressources dans un même dossier.
	
		downloaderv3 --urls-file pages.url --output-dir destination/
	
	Les fichiers contenant les adresses doivent contenir une URL par ligne avec le nom du fichier à sauvegarder avant l'URL, suivi de ":" :
	
		test.html:http://www.example.org/f1.html
		test2.html:http://www.example.org/f2.html

	On peut spécifier en otion un délai global, en secondes, inter-téléchargements et un délai spécifique, toujours en secondes, pour un site donné :
	
		--global-delay 2 --specific-delay www.liberation.fr 3 --specific-delay www.lefigaro.fr 1

	Note : en cas d'URL contenant une "image:data" le fichier est décodé sans requête HTTP.

	v1.0, 2016-01-24, (c)FB

	Ce programme a eu une vie PHP (trop lente), une version Node (trop bordélique) et je termine ici avec une version Go qui fonctionne parfaitement, qui est lisible et légère.
*/

type PageRequest struct {
    url string
    filePath string
    stdout	bool
    //
    fileSize    int
    httpCode    int
    body        string
}

var	DefaultUserAgent string = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_11_3) AppleWebKit/601.4.4 (KHTML, like Gecko) Version/9.0.3 Safari/601.4.4"
var	DefaultProxy string = ""

//
var	gProxy string = DefaultProxy
var	gUserAgent string = DefaultUserAgent

var GlobalTransport = &http.Transport{
	/*DisableCompression: true,*/
	Proxy: func(r *http.Request) (*url.URL, error)		{
		if len(gProxy) == 0		{
			return nil, nil
		}
		return url.Parse(gProxy)
	},
}
var GlobalHTTPClient = &http.Client{
	Timeout:30*time.Second,
	Transport:GlobalTransport,
}

func (r *PageRequest) load(done chan int, verbose bool, quietOnError bool)       {
	if (verbose)	{
		fmt.Println("PageRequest::load : début du téléchargement de", r.url, ", dans le fichier", r.filePath)
	}
	//
    //J'ai remplacé la ligne "Ret, err := http.Get(r.url)" par les trois lignes suivantes, pour gérer un user-agent paramétrable (voire même le future referer) :
	req, err := http.NewRequest("GET", r.url, nil)
	req.Header.Add("User-Agent", gUserAgent)
	Ret, err := GlobalHTTPClient.Do(req)
	//
	defer func()	{
		if err == nil	{
			Ret.Body.Close()
		}
		done <- 1
	}()
	r.httpCode = -1
	if Ret != nil	{		//Si la page a une URL dont l'hôte est introuvable par le DNS, Ret est nil, on ne peut donc pas déréférencer le membre StatusCode de cet objet...
		r.httpCode = Ret.StatusCode
	}
    if err != nil       {
	    if quietOnError == false	{
    	    fmt.Println("PageRequest : erreur durant le chargement de", r.url, "avec le code", r.httpCode, " et l'erreur", err)
    	}
        return
    }
    body, err := ioutil.ReadAll(Ret.Body)
    if err != nil       {
	    if quietOnError == false	{
	        fmt.Println("PageRequest : erreur durant la lecture-mémoire de la réponse de", r.url)
	    }
        return
    }
    r.fileSize = len(body)
    if r.httpCode != 200	{
	    if quietOnError == false	{
		    fmt.Println("PageRequest : la ressource a sauvergarder en", r.filePath, "et d'URL", r.url, "a été chargée (Retour :",  r.httpCode, "), sa taille est de", r.fileSize)
		}
		//Si la requête a générée une erreur irrécupérable, on va remplacer ce que le serveur a renvoyé (i.e. la page d'erreur) par 3 caractères formant le code d'erreur HTTP
	    switch 	{
		    case r.httpCode>=400 && r.httpCode<500:
				body = []byte(strconv.Itoa(r.httpCode))
		    case r.httpCode>=500 && r.httpCode<600:
				body = []byte(strconv.Itoa(r.httpCode))
		    default:
				body = []byte(string(r.httpCode))
	    }
    }
    //
    if r.stdout == false		{
	    err = ioutil.WriteFile(r.filePath, body, 0777)
	    if err != nil       {
		    if quietOnError == false	{
		        fmt.Println("PageRequest : erreur durant la sauvegarde du fichier", r.filePath, "correspondant au flux", r.url)
		    }
	    }
    }	else	{
	    fmt.Printf("%s\n", body)
    }
}

type ActionMessage struct	{
	QueueIndex int
	QueueName string
	QueueEmpty bool
	url string
}

func	launchDownloadQueue(queueName string, queueIndex int, q []PageRequest, globalCompletionChan chan ActionMessage, verbose bool, quietOnError bool, delayBetweenDownloads float64)		{
    doneDownloads := make(chan int)      //Channel pour la complétion de chacun des téléchargements de la queue
    //
    for Index, req := range q		{
		go req.load(doneDownloads, verbose, quietOnError)
		<- doneDownloads			//On attend la fin du téléchargement. Le but de ce programme est de ne pas trop stresser les sites.
		if verbose	{
			fmt.Println("Fin du téléchargement de", req.url, "avec le retour", req.httpCode)
		}
		globalCompletionChan <- ActionMessage{queueIndex, queueName, false, req.url}	//On vient de finir un téléchargement
		if Index == len(q)-1	 	{	break	}
		if verbose	{
			fmt.Println("Queue", queueIndex, "Délai entre les téléchargements :", delayBetweenDownloads, "sec")
			fmt.Println("Queue", queueIndex, "Avant le sleep", time.Now())
		}
		time.Sleep(time.Duration(delayBetweenDownloads) * time.Second)
		if verbose	{
			fmt.Println("Queue", queueIndex, "Après le sleep", time.Now())
		}
    }
	globalCompletionChan <- ActionMessage{queueIndex, queueName, true, ""}			//La queue vient de se vider : tous les téléchargements sont achevés
}

//
func checkIfURLIsDataURL(testURL string, verbose bool) ([]byte, error) 	{
//	MarkerPos := strings.Index(testURL, "data:image/")
	MarkerPos := strings.Index(testURL, "/data:")
	if MarkerPos == -1	{
		return nil, nil
	}
	testURL = testURL[MarkerPos:]
    dataURL, err := dataurl.DecodeString(testURL)
    if err != nil {
        fmt.Println(err)
        return nil, err
    }
    if verbose		{
	    fmt.Printf("content type: %s, data size: %d\n", dataURL.MediaType.ContentType(), len(dataURL.Data))
	}
	return dataURL.Data, nil
}

//Détecte si un dossier ou un fichier existe à l'endroit pointé par le chemin "path"
func exists(path string) bool {
    _, err := os.Stat(path)
    if err == nil { return true }
    if os.IsNotExist(err) { return false }
    return true
}

func 	downloadEverything(urlsAsText string, outputDir string, outputToSTDOut bool, verbose bool, quietOnError bool, nbURLsToHandle int, specificDelays map[string]float64, globalDelay float64)		{
	Queues := make(map[string][]PageRequest)

	//On va analyser les URLs d'entrée et on va remplir les queues de téléchargement
	Lines := strings.Split(urlsAsText, "\n")
	NbValidURLs := 0
	for LineIndex, Line := range Lines      {
		Line = strings.TrimSpace(Line)
		if len(Line) == 0		{	continue	}
		//On a une Id et une URL séparées par ":"
	    LineComps := strings.SplitN(Line, ":", 2)
	    if len(LineComps) < 2 || LineComps == nil		{
		    if quietOnError == false	{
			    fmt.Println("Erreur sur la ligne", LineIndex)
			}
		    continue;
	    }
		PageFileName, PotentialURL := LineComps[0], LineComps[1]
		PotentialURL = strings.TrimSpace(PotentialURL)
		if len(PotentialURL) == 0		{	continue	}

		//
		TargetFilePath := outputDir+"/"+PageFileName
		TargetURL, _ := url.Parse(PotentialURL)

		//Si l'image est inclue dans l'URL via le schéma image:data, on sauve directement le fichier sans avoir besoin de télécharger
		ImageData, _ := checkIfURLIsDataURL(TargetURL.Path, verbose)
		if ImageData != nil		{
			fmt.Println("On sauve le fichier", TargetFilePath, "à partir des données image:data représentant", len(ImageData), "octet(s)")
			ioutil.WriteFile(TargetFilePath, ImageData, 0777)
			continue
		}
		//
		if len(TargetURL.Scheme) == 0		{
		    if quietOnError == false	{
				fmt.Println("L'URL", PotentialURL, "est invalide")
			}
			continue
		}

		//On ne lance le téléchargement QUE si le fichier n'existe pas encore, sinon on risquerait d'effacer un téléchargement précédent
		if exists(TargetFilePath) == false		{
			if verbose	{
				fmt.Println(TargetFilePath, "n'existe pas...")
			}
			_, ok := Queues[TargetURL.Host]
			if ok == false	{	Queues[TargetURL.Host] = make([]PageRequest, 0)		}
			TargetQueue, _ := Queues[TargetURL.Host]
			NewRequest := PageRequest{url:PotentialURL, filePath:TargetFilePath, stdout:outputToSTDOut}
			Queues[TargetURL.Host] = append(TargetQueue, NewRequest)

			//
			NbValidURLs++
			if nbURLsToHandle != -1 && NbValidURLs == nbURLsToHandle		{
				fmt.Println("Limite de", nbURLsToHandle, "URL(s) atteinte")
				break
			}
		}	else	{
			if verbose	{
				fmt.Println("Le fichier", TargetFilePath, "existe déjà, je ne veux pas le re-télécharger.")
			}
		}
	}
	fmt.Println("Trouvé :", NbValidURLs, "adresses réparties sur", len(Queues), "queues")

	//On lance le téléchargement de chacune des queues en parallèle
	Downloads := make(chan ActionMessage)
	QueueIndex := 0
	for QueueName, Queue := range Queues	{
		DelayBetweenDownloads, ok := specificDelays[QueueName]
		if ok == false	{
			DelayBetweenDownloads = globalDelay
		}
		go launchDownloadQueue(QueueName, QueueIndex, Queue, Downloads, verbose, quietOnError, DelayBetweenDownloads)
		QueueIndex++
	}

	//On reçoit le produit des téléchargements de toutes les queues
	if QueueIndex > 0		{
		NbDownloads := 0
		NbEmptiedQueues := 0
		for 	{
			Received := <- Downloads
			if Received.QueueEmpty == true		{
				if verbose		{
					fmt.Println("Queue", Received.QueueIndex, "/", Received.QueueName, "terminée")
				}
				NbEmptiedQueues++
				if NbEmptiedQueues == len(Queues)		{
					break
				}
			}
			if Received.QueueEmpty == false		{
				if verbose	{
					fmt.Println("Queue", Received.QueueIndex, "/", Received.QueueName, ", téléchargement terminé :", Received.url)
				}
				NbDownloads++
			}
		}
		fmt.Println("Les", len(Queues), "queues viennent de se vider après", NbDownloads, "téléchargements")
	}
}


//Fonction équivalente de php::file_get_contents
func ReadTextFile(filePath string) (string, error)      {
    b, err := ioutil.ReadFile(filePath)
    if err != nil {		return "", err 	}
    return string(b), nil
}

func main() {
	OutputDir := ""
	URLsFilePath := ""
	SingleURL := ""
	OutputToSTDOut := false
	Verbose := false
	QuietOnError := false
	NbURLsToHandle := -1		//-1 : on télécharge toutes les URLs passées en paramètres
	SpecificDelays := make(map[string]float64)
	var GlobalDelay float64 = 1.0
	//
	idx := 1
	for 	{
		switch (os.Args[idx])		{
			case "--urls-file":
				idx += 1
				URLsFilePath = os.Args[idx]
			case "--single-url":
				idx += 1
				SingleURL = os.Args[idx]
			case "--output-dir":
				idx += 1
				OutputDir = os.Args[idx]
			case "--verbose":
				Verbose = true
			case "--quiet":
				QuietOnError = true
			case "--limit":
				idx += 1
				NbURLsToHandle, _ = strconv.Atoi(os.Args[idx])
			case "--global-delay":
				idx += 1
				GlobalDelay, _ = strconv.ParseFloat(os.Args[idx], 64)
			case "--specific-delay":
				idx += 1
				TargetDomain := os.Args[idx]
				idx += 1
				SpecificDelay, _ := strconv.ParseFloat(os.Args[idx], 64)
				SpecificDelays[TargetDomain] = SpecificDelay
			case "--no-proxy":
				gProxy = ""
			case "--proxy":
				idx += 1
				gProxy = os.Args[idx]
			case "--user-agent":
				idx += 1
				gUserAgent = os.Args[idx]
		}
		//
		idx++
		if (idx == len(os.Args))		{	break	}
	}

	//Analyse des paramètres précédents
	if len(URLsFilePath)>0 && len(OutputDir)==0		{
		fmt.Println("Le fichier d'URL *", URLsFilePath, "* existe mais il manque un dossier pour le stockage des téléchargements : --output-dir")
		os.Exit(2)
	}
	if len(URLsFilePath)!=0 && len(SingleURL)!=0		{
		fmt.Println("Paramètres contradictoires : on doit télécharger les URLs du fichier *", URLsFilePath, "* et l'URL ", SingleURL)
		os.Exit(2)
	}
	if len(SingleURL)>0 && len(OutputDir)==0		{
		OutputToSTDOut = true
	}
	//
	if len(URLsFilePath)==0 && len(SingleURL)==0 && len(OutputDir)!=0		{
		fmt.Println("J'ai un dossier de stockage mais pas de fichier d'URLs ni d'URL unique à télécharger...")
		os.Exit(1)
	}
	if len(URLsFilePath)==0 && len(SingleURL)==0 && len(OutputDir)==0		{
		fmt.Println("Rien à faire. Je sors.")
		os.Exit(1)
	}

	//On vérifie que URLsFilePath existe, tout comme OutputDir
	if len(URLsFilePath)!=0 && exists(URLsFilePath)==false		{
		fmt.Println("Le fichier des URLs à télécharger *", URLsFilePath, "* n'existe pas.")
		os.Exit(2)
	}
	if len(OutputDir)!=0 && exists(OutputDir)==false		{
		fmt.Println("Le dossier de stockage *", OutputDir, "* n'existe pas.")
		os.Exit(2)
	}

	//Espèce de mode verbose
	if len(URLsFilePath)!=0		{
		fmt.Println("On va télécharger les URLs du fichier", URLsFilePath);
	}	else	{
		fmt.Println("On va télécharger l'URL unique", SingleURL);
	}
	if len(OutputDir)!=0		{
		fmt.Println("Le dossier destination sera", OutputDir)
	}	else	{
		fmt.Println("La sortie est sur stdout")
	}

	//
	var	URLList string = ""
	var	err interface{} = nil
	if len(URLsFilePath)!=0		{
		URLList, err = ReadTextFile(URLsFilePath)
		if err != nil		{
			fmt.Println("Impossible d'accéder au fichier *", URLsFilePath, "* :", err)
			os.Exit(3)
		}
	}
	if len(SingleURL)!=0		{
		URLList = "1:"+SingleURL
	}

	//
	downloadEverything(URLList, OutputDir, OutputToSTDOut, Verbose, QuietOnError, NbURLsToHandle, SpecificDelays, GlobalDelay)
}
